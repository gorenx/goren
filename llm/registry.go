package llm

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"

	"github.com/gorenx/goren/plugin"
)

type adapterRoute struct {
	backend  Adapter
	metadata ProviderInfo
	policy   RetryPolicy
}

type adapterRegistrationState struct {
	owner    *runtimeService
	backend  Adapter
	owned    []string
	disposed bool
	cleanup  plugin.Disposer
}

type adapterHandle struct {
	state *adapterRegistrationState
}

type directoryRegistrationState struct {
	owner    *runtimeService
	held     []ConfigurableProvider
	disposed bool
	cleanup  plugin.Disposer
}

type directoryHandle struct {
	state *directoryRegistrationState
}

type discoveryEntry struct {
	backend ModelDiscovery
}

type runtimeService struct {
	sourceScope *plugin.Scope
	reporter    ObserverFailureReporter

	mu             sync.RWMutex
	routes         map[string]*adapterRoute
	routeOrder     []string
	directory      map[string]ConfigurableProvider
	directoryOrder []string
	discoveries    map[string]*discoveryEntry
}

// NewRuntime creates the Harness LLM service owned by sourceScope.
func NewRuntime(sourceScope *plugin.Scope, reporter ObserverFailureReporter) (LlmRuntime, error) {
	if sourceScope == nil {
		return nil, errors.New("llm: source scope is nil")
	}
	return &runtimeService{
		sourceScope: sourceScope, reporter: reporter,
		routes: make(map[string]*adapterRoute), directory: make(map[string]ConfigurableProvider),
		discoveries: make(map[string]*discoveryEntry),
	}, nil
}

func (owner *runtimeService) RegisterAdapter(
	requestContext context.Context,
	ownerScope *plugin.Scope,
	providers []string,
	backend Adapter,
) (AdapterRegistrationHandle, error) {
	if ownerScope == nil {
		return nil, errors.New("llm: adapter owner scope is nil")
	}
	if backend == nil {
		return nil, errors.New("llm: adapter is nil")
	}
	backendValue := reflect.ValueOf(backend)
	if backendValue.Kind() != reflect.Pointer || backendValue.IsNil() {
		return nil, MustLlmError("an adapter must be a non-nil pointer so instance ownership is stable", "INVALID_ADAPTER")
	}
	if len(providers) == 0 {
		return nil, MustLlmError("an adapter must register at least one provider", "INVALID_ADAPTER")
	}
	candidates, err := owner.describeRoutes(providers, backend)
	if err != nil {
		return nil, err
	}
	state := &adapterRegistrationState{owner: owner, backend: backend}
	ownedRelease, err := plugin.Own(ownerScope, "llm.registerAdapter()", state.releaseOwned)
	if err != nil {
		return nil, err
	}
	state.cleanup = ownedRelease
	if err := owner.commitRoutes(state, candidates); err != nil {
		return nil, errors.Join(err, ownedRelease(requestContext))
	}
	if err := owner.publishUpdate(requestContext); err != nil {
		return nil, errors.Join(err, ownedRelease(requestContext))
	}
	return &adapterHandle{state: state}, nil
}

func (owner *runtimeService) describeRoutes(providers []string, backend Adapter) ([]adapterRoute, error) {
	unique := make(map[string]struct{}, len(providers))
	candidates := make([]adapterRoute, 0, len(providers))
	for _, providerRoute := range providers {
		if providerRoute == "" {
			return nil, MustLlmError("adapter provider names must be non-empty", "INVALID_ADAPTER")
		}
		if _, exists := unique[providerRoute]; exists {
			return nil, MustLlmError(fmt.Sprintf("an adapter for provider %q is already registered", providerRoute), "DUPLICATE_ADAPTER")
		}
		unique[providerRoute] = struct{}{}
		metadata := ProviderInfo{ID: providerRoute, Name: providerRoute}
		if describer, ok := backend.(ProviderDescriber); ok {
			var err error
			metadata, err = describer.DescribeProvider(providerRoute)
			if err != nil {
				return nil, err
			}
		}
		if metadata.ID != providerRoute || metadata.Name == "" {
			return nil, MustLlmError(
				fmt.Sprintf("adapter metadata for provider %q must preserve its id and have a non-empty name", providerRoute),
				"INVALID_ADAPTER",
			)
		}
		policy, err := ResolveRetryPolicy(nil, fmt.Sprintf("llm: provider %q retryPolicy", providerRoute))
		if err != nil {
			return nil, err
		}
		if policySource, ok := backend.(RetryPolicyProvider); ok {
			candidatePolicy, policyErr := policySource.ProviderRetryPolicy(providerRoute)
			if policyErr != nil {
				return nil, policyErr
			}
			if candidatePolicy != nil {
				policy = candidatePolicy.CloneRetryPolicy()
			}
		}
		if err := validateResolvedPolicy(policy); err != nil {
			return nil, MustLlmError(fmt.Sprintf("invalid retry policy for provider %q: %v", providerRoute, err), "INVALID_ADAPTER")
		}
		candidates = append(candidates, adapterRoute{backend: backend, metadata: metadata, policy: policy})
	}
	return candidates, nil
}

func validateResolvedPolicy(policy RetryPolicy) error {
	if policy == nil {
		return errors.New("policy is nil")
	}
	backoff := policy.RetryBackoff()
	if !finitePositive(backoff.InitialDelayMS) || !finitePositive(backoff.MaxDelayMS) ||
		backoff.InitialDelayMS > backoff.MaxDelayMS || backoff.MaxDelayMS > MaxTimerDelayMS ||
		backoff.JitterRatio < 0 || backoff.JitterRatio > 1 {
		return errors.New("backoff is invalid")
	}
	switch typedPolicy := policy.(type) {
	case NormalRetryPolicy:
		if typedPolicy.MaxRetries < 0 || typedPolicy.MaxRetries > maxJavaScriptSafeInteger {
			return errors.New("maxRetries is invalid")
		}
		return validateRetryableCodes(typedPolicy.RetryableCodes, "retryPolicy")
	case AlwaysRetryPolicy:
		return nil
	default:
		return errors.New("policy implementation is unsupported")
	}
}

func (owner *runtimeService) commitRoutes(state *adapterRegistrationState, candidates []adapterRoute) error {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if state.disposed {
		return MustLlmError("a disposed adapter registration cannot replace its routes", "REGISTRATION_DISPOSED")
	}
	ownedSet := make(map[string]struct{}, len(state.owned))
	for _, providerRoute := range state.owned {
		ownedSet[providerRoute] = struct{}{}
	}
	for _, candidate := range candidates {
		providerRoute := candidate.metadata.ID
		if _, held := owner.routes[providerRoute]; held {
			if _, own := ownedSet[providerRoute]; !own {
				return MustLlmError(fmt.Sprintf("an adapter for provider %q is already registered", providerRoute), "DUPLICATE_ADAPTER")
			}
		}
	}
	for _, providerRoute := range state.owned {
		delete(owner.routes, providerRoute)
		owner.routeOrder = slices.DeleteFunc(owner.routeOrder, func(existingRoute string) bool {
			return existingRoute == providerRoute
		})
	}
	state.owned = state.owned[:0]
	for index := range candidates {
		candidate := candidates[index]
		providerRoute := candidate.metadata.ID
		owner.routes[providerRoute] = &candidate
		owner.routeOrder = append(owner.routeOrder, providerRoute)
		state.owned = append(state.owned, providerRoute)
	}
	return nil
}

func (state *adapterRegistrationState) releaseOwned(closeContext context.Context) error {
	owner := state.owner
	owner.mu.Lock()
	if state.disposed {
		owner.mu.Unlock()
		return nil
	}
	state.disposed = true
	changed := len(state.owned) != 0
	for _, providerRoute := range state.owned {
		delete(owner.routes, providerRoute)
		owner.routeOrder = slices.DeleteFunc(owner.routeOrder, func(existingRoute string) bool {
			return existingRoute == providerRoute
		})
	}
	state.owned = nil
	owner.mu.Unlock()
	if changed {
		return owner.publishUpdate(closeContext)
	}
	return nil
}

func (handleState *adapterHandle) Replace(requestContext context.Context, providers []string) error {
	if handleState == nil || handleState.state == nil {
		return MustLlmError("a disposed adapter registration cannot replace its routes", "REGISTRATION_DISPOSED")
	}
	candidates, err := handleState.state.owner.describeRoutes(providers, handleState.state.backend)
	if err != nil {
		return err
	}
	if err := handleState.state.owner.commitRoutes(handleState.state, candidates); err != nil {
		return err
	}
	return handleState.state.owner.publishUpdate(requestContext)
}

func (handleState *adapterHandle) Release(closeContext context.Context) error {
	if handleState == nil || handleState.state == nil || handleState.state.cleanup == nil {
		return nil
	}
	return handleState.state.cleanup(closeContext)
}

func (owner *runtimeService) ListProviders() []ProviderInfo {
	owner.mu.RLock()
	defer owner.mu.RUnlock()
	result := make([]ProviderInfo, 0, len(owner.routeOrder))
	for _, providerRoute := range owner.routeOrder {
		if record := owner.routes[providerRoute]; record != nil {
			result = append(result, record.metadata)
		}
	}
	return result
}

func (owner *runtimeService) RegisterConfigurableProviders(
	requestContext context.Context,
	ownerScope *plugin.Scope,
	entries []ConfigurableProvider,
) (DirectoryRegistrationHandle, error) {
	if ownerScope == nil {
		return nil, errors.New("llm: configurable-provider owner scope is nil")
	}
	if len(entries) == 0 {
		return nil, MustLlmError("a configurable-provider registration must declare at least one provider", "INVALID_DIRECTORY")
	}
	candidates, err := validateDirectoryEntries(entries)
	if err != nil {
		return nil, err
	}
	state := &directoryRegistrationState{owner: owner}
	ownedRelease, err := plugin.Own(ownerScope, "llm.registerConfigurableProviders()", state.releaseOwned)
	if err != nil {
		return nil, err
	}
	state.cleanup = ownedRelease
	if err := owner.commitDirectory(state, candidates); err != nil {
		return nil, errors.Join(err, ownedRelease(requestContext))
	}
	if err := owner.publishUpdate(requestContext); err != nil {
		return nil, errors.Join(err, ownedRelease(requestContext))
	}
	return &directoryHandle{state: state}, nil
}

func validateDirectoryEntries(entries []ConfigurableProvider) ([]ConfigurableProvider, error) {
	seen := make(map[string]struct{}, len(entries))
	detached := make([]ConfigurableProvider, 0, len(entries))
	for _, entry := range entries {
		if entry.Provider == "" || entry.DisplayName == "" || entry.SettingsNS == "" {
			return nil, MustLlmError("configurable providers need a non-empty provider, displayName, and settingsNs", "INVALID_DIRECTORY")
		}
		for _, segment := range entry.SettingsPath {
			if segment == "" {
				return nil, MustLlmError(fmt.Sprintf("configurable provider %q has an empty settingsPath segment", entry.Provider), "INVALID_DIRECTORY")
			}
		}
		if _, exists := seen[entry.Provider]; exists {
			return nil, MustLlmError(fmt.Sprintf("configurable provider %q is already declared", entry.Provider), "DUPLICATE_DIRECTORY")
		}
		seen[entry.Provider] = struct{}{}
		entry.SettingsPath = slices.Clone(entry.SettingsPath)
		entry.Declared = cloneBool(entry.Declared)
		detached = append(detached, entry)
	}
	return detached, nil
}

func (owner *runtimeService) commitDirectory(state *directoryRegistrationState, candidates []ConfigurableProvider) error {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if state.disposed {
		return MustLlmError("this configurable-provider registration was disposed", "REGISTRATION_DISPOSED")
	}
	ownedSet := make(map[string]struct{}, len(state.held))
	for _, entry := range state.held {
		ownedSet[entry.Provider] = struct{}{}
	}
	for _, candidate := range candidates {
		if _, held := owner.directory[candidate.Provider]; held {
			if _, own := ownedSet[candidate.Provider]; !own {
				return MustLlmError(fmt.Sprintf("configurable provider %q is already declared", candidate.Provider), "DUPLICATE_DIRECTORY")
			}
		}
	}
	for _, entry := range state.held {
		delete(owner.directory, entry.Provider)
		owner.directoryOrder = slices.DeleteFunc(owner.directoryOrder, func(providerRoute string) bool {
			return providerRoute == entry.Provider
		})
	}
	state.held = nil
	for _, candidate := range candidates {
		owner.directory[candidate.Provider] = candidate
		owner.directoryOrder = append(owner.directoryOrder, candidate.Provider)
		state.held = append(state.held, candidate)
	}
	return nil
}

func (state *directoryRegistrationState) releaseOwned(closeContext context.Context) error {
	owner := state.owner
	owner.mu.Lock()
	if state.disposed {
		owner.mu.Unlock()
		return nil
	}
	state.disposed = true
	changed := len(state.held) != 0
	for _, entry := range state.held {
		delete(owner.directory, entry.Provider)
		owner.directoryOrder = slices.DeleteFunc(owner.directoryOrder, func(providerRoute string) bool {
			return providerRoute == entry.Provider
		})
	}
	state.held = nil
	owner.mu.Unlock()
	if changed {
		return owner.publishUpdate(closeContext)
	}
	return nil
}

func (handleState *directoryHandle) Replace(requestContext context.Context, entries []ConfigurableProvider) error {
	if handleState == nil || handleState.state == nil {
		return MustLlmError("this configurable-provider registration was disposed", "REGISTRATION_DISPOSED")
	}
	candidates, err := validateDirectoryEntries(entries)
	if err != nil {
		return err
	}
	if err := handleState.state.owner.commitDirectory(handleState.state, candidates); err != nil {
		return err
	}
	return handleState.state.owner.publishUpdate(requestContext)
}

func (handleState *directoryHandle) Release(closeContext context.Context) error {
	if handleState == nil || handleState.state == nil || handleState.state.cleanup == nil {
		return nil
	}
	return handleState.state.cleanup(closeContext)
}

func (owner *runtimeService) ListConfigurableProviders() []ConfigurableProvider {
	owner.mu.RLock()
	defer owner.mu.RUnlock()
	result := make([]ConfigurableProvider, 0, len(owner.directoryOrder))
	for _, providerRoute := range owner.directoryOrder {
		entry, found := owner.directory[providerRoute]
		if !found {
			continue
		}
		entry.SettingsPath = slices.Clone(entry.SettingsPath)
		entry.Declared = cloneBool(entry.Declared)
		result = append(result, entry)
	}
	return result
}

func (owner *runtimeService) RegisterModelDiscovery(
	ownerScope *plugin.Scope,
	settingsNS string,
	backend ModelDiscovery,
) (plugin.Disposer, error) {
	if ownerScope == nil {
		return nil, errors.New("llm: model discovery owner scope is nil")
	}
	if strings.TrimSpace(settingsNS) == "" {
		return nil, MustLlmError("model discovery needs a non-empty settings namespace", "INVALID_DISCOVERY")
	}
	if backend == nil {
		return nil, MustLlmError("model discovery callback is nil", "INVALID_DISCOVERY")
	}
	record := &discoveryEntry{backend: backend}
	owner.mu.Lock()
	if owner.discoveries[settingsNS] != nil {
		owner.mu.Unlock()
		return nil, MustLlmError(fmt.Sprintf("model discovery for %q is already registered", settingsNS), "DUPLICATE_DISCOVERY")
	}
	owner.discoveries[settingsNS] = record
	owner.mu.Unlock()
	ownedRelease, err := plugin.Own(ownerScope, "llm.registerModelDiscovery()", func(context.Context) error {
		owner.mu.Lock()
		if owner.discoveries[settingsNS] == record {
			delete(owner.discoveries, settingsNS)
		}
		owner.mu.Unlock()
		return nil
	})
	if err != nil {
		owner.mu.Lock()
		if owner.discoveries[settingsNS] == record {
			delete(owner.discoveries, settingsNS)
		}
		owner.mu.Unlock()
		return nil, err
	}
	return ownedRelease, nil
}

func (owner *runtimeService) DiscoverModels(
	requestContext context.Context,
	settingsNS string,
	request ModelDiscoveryRequest,
) ([]DiscoveredModel, error) {
	owner.mu.RLock()
	record := owner.discoveries[settingsNS]
	owner.mu.RUnlock()
	if record == nil {
		return nil, MustLlmError(fmt.Sprintf("no model discovery is registered for %q", settingsNS), "NO_DISCOVERY")
	}
	if request.Provider == "" && request.BaseURL == "" {
		return nil, MustLlmError("model discovery needs a provider route or a baseURL", "INVALID_DISCOVERY")
	}
	discovered, err := record.backend.Discover(requestContext, request)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(discovered))
	result := make([]DiscoveredModel, 0, len(discovered))
	for _, candidate := range discovered {
		if candidate.ID == "" {
			continue
		}
		if _, exists := seen[candidate.ID]; exists {
			continue
		}
		seen[candidate.ID] = struct{}{}
		candidate.ContextWindow = cloneInt(candidate.ContextWindow)
		candidate.MaxTokens = cloneInt(candidate.MaxTokens)
		result = append(result, candidate)
	}
	return result, nil
}

func (owner *runtimeService) publishUpdate(requestContext context.Context) error {
	dispatchErr := plugin.EmitFrom(requestContext, owner.sourceScope, AdaptersUpdated, struct{}{})
	if dispatchErr == nil {
		return nil
	}
	if errorTreeHasCode(dispatchErr, "INVARIANT") {
		return dispatchErr
	}
	owner.reportContained(dispatchErr)
	return nil
}

func (owner *runtimeService) reportContained(dispatchErr error) {
	if owner.reporter == nil {
		return
	}
	defer func() {
		_ = recover()
	}()
	owner.reporter(dispatchErr)
}

func errorTreeHasCode(problem error, targetCode string) bool {
	if problem == nil {
		return false
	}
	if carrier, ok := problem.(interface{ Code() string }); ok && carrier.Code() == targetCode {
		return true
	}
	if joined, ok := problem.(interface{ Unwrap() []error }); ok {
		for _, nested := range joined.Unwrap() {
			if errorTreeHasCode(nested, targetCode) {
				return true
			}
		}
		return false
	}
	return errorTreeHasCode(errors.Unwrap(problem), targetCode)
}

func cloneBool(source *bool) *bool {
	if source == nil {
		return nil
	}
	value := *source
	return &value
}
