package llm

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"

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
