package llm

import (
	"fmt"
	"reflect"
	"slices"

	"github.com/google/uuid"
)

// RegistrationID identifies a registration independently of any actor activation.
// IDs are never reused; removing an old ID cannot affect a replacement registration.
type RegistrationID string

// Registry owns provider registrations. Its zero value is ready for serialized use.
// It performs no model I/O, publishes no events, and retains no caller references.
type Registry struct {
	routes         map[string]*Binding
	adapters       map[RegistrationID]adapterRegistration
	routeOrder     []string
	directories    map[RegistrationID][]ConfigurableProvider
	directoryOrder []RegistrationID
}

type adapterRegistration struct {
	backend   Adapter
	providers []string
}

// RegisterAdapter atomically acquires every declared provider route.
func (catalog *Registry) RegisterAdapter(providers []string, backend Adapter) (RegistrationID, error) {
	if backend == nil {
		return "", MustLlmError("adapter is nil", "INVALID_ADAPTER")
	}
	candidate := reflect.ValueOf(backend)
	if candidate.Kind() != reflect.Pointer || candidate.IsNil() {
		return "", MustLlmError("an adapter must be a non-nil pointer", "INVALID_ADAPTER")
	}
	if len(providers) == 0 {
		return "", MustLlmError("an adapter must register at least one provider", "INVALID_ADAPTER")
	}
	routes, err := describeRoutes(providers, backend)
	if err != nil {
		return "", err
	}
	id := RegistrationID(uuid.NewString())
	if err := catalog.installAdapter(id, backend, routes); err != nil {
		return "", err
	}
	return id, nil
}

// ReplaceAdapter validates the complete route set before replacing a registration.
func (catalog *Registry) ReplaceAdapter(id RegistrationID, providers []string) error {
	held, found := catalog.adapters[id]
	if !found {
		return MustLlmError("adapter registration no longer exists", "REGISTRATION_DISPOSED")
	}
	routes, err := describeRoutes(providers, held.backend)
	if err != nil {
		return err
	}
	return catalog.installAdapter(id, held.backend, routes)
}

func (catalog *Registry) installAdapter(id RegistrationID, backend Adapter, candidates []Binding) error {
	held := catalog.adapters[id]
	for _, candidate := range candidates {
		if catalog.routes[candidate.metadata.ID] != nil && !slices.Contains(held.providers, candidate.metadata.ID) {
			return MustLlmError(fmt.Sprintf("an adapter for provider %q is already registered", candidate.metadata.ID), "DUPLICATE_ADAPTER")
		}
	}
	catalog.RemoveAdapter(id)
	if catalog.routes == nil {
		catalog.routes = make(map[string]*Binding)
	}
	if catalog.adapters == nil {
		catalog.adapters = make(map[RegistrationID]adapterRegistration)
	}
	providers := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		catalog.routes[candidate.metadata.ID] = &candidate
		catalog.routeOrder = append(catalog.routeOrder, candidate.metadata.ID)
		providers = append(providers, candidate.metadata.ID)
	}
	catalog.adapters[id] = adapterRegistration{
		backend:   backend,
		providers: providers,
	}
	return nil
}

// RemoveAdapter releases only this registration and reports whether it existed.
func (catalog *Registry) RemoveAdapter(id RegistrationID) bool {
	held, found := catalog.adapters[id]
	if !found {
		return false
	}
	for _, provider := range held.providers {
		delete(catalog.routes, provider)
	}
	catalog.routeOrder = slices.DeleteFunc(catalog.routeOrder, func(provider string) bool { return slices.Contains(held.providers, provider) })
	delete(catalog.adapters, id)
	return true
}

// Select fixes the adapter and replay-compatible aliases for a new call.
func (catalog *Registry) Select(provider string) (*Binding, error) {
	held := catalog.routes[provider]
	if held == nil {
		return nil, MustLlmError(fmt.Sprintf("no adapter registered for provider %q", provider), "NO_ADAPTER")
	}
	detached := *held
	detached.policy = held.policy.CloneRetryPolicy()
	detached.replayProviders = nil
	for _, route := range catalog.routeOrder {
		if catalog.routes[route].backend == held.backend {
			detached.replayProviders = append(detached.replayProviders, route)
		}
	}
	return &detached, nil
}

// ListProviders returns provider metadata in registration order.
func (catalog *Registry) ListProviders() []ProviderInfo {
	result := make([]ProviderInfo, 0, len(catalog.routeOrder))
	for _, route := range catalog.routeOrder {
		result = append(result, catalog.routes[route].metadata)
	}
	return result
}

// RegisterConfigurableProviders installs one independently owned directory set.
func (catalog *Registry) RegisterConfigurableProviders(entries []ConfigurableProvider) (RegistrationID, error) {
	if len(entries) == 0 {
		return "", MustLlmError("a directory must declare at least one provider", "INVALID_DIRECTORY")
	}
	id := RegistrationID(uuid.NewString())
	if err := catalog.installDirectory(id, entries); err != nil {
		return "", err
	}
	return id, nil
}

// ReplaceConfigurableProviders atomically replaces an existing directory set.
func (catalog *Registry) ReplaceConfigurableProviders(id RegistrationID, entries []ConfigurableProvider) error {
	if _, found := catalog.directories[id]; !found {
		return MustLlmError("directory registration no longer exists", "REGISTRATION_DISPOSED")
	}
	return catalog.installDirectory(id, entries)
}

func (catalog *Registry) installDirectory(id RegistrationID, entries []ConfigurableProvider) error {
	candidates, err := validateDirectoryEntries(entries)
	if err != nil {
		return err
	}
	for other, held := range catalog.directories {
		if other == id {
			continue
		}
		for _, candidate := range candidates {
			if slices.ContainsFunc(held, func(entry ConfigurableProvider) bool { return entry.Provider == candidate.Provider }) {
				return MustLlmError(fmt.Sprintf("configurable provider %q is already declared", candidate.Provider), "DUPLICATE_DIRECTORY")
			}
		}
	}
	catalog.RemoveConfigurableProviders(id)
	if catalog.directories == nil {
		catalog.directories = make(map[RegistrationID][]ConfigurableProvider)
	}
	catalog.directories[id] = candidates
	catalog.directoryOrder = append(catalog.directoryOrder, id)
	return nil
}

// RemoveConfigurableProviders releases only the specified directory registration.
func (catalog *Registry) RemoveConfigurableProviders(id RegistrationID) bool {
	if _, found := catalog.directories[id]; !found {
		return false
	}
	delete(catalog.directories, id)
	catalog.directoryOrder = slices.DeleteFunc(catalog.directoryOrder, func(other RegistrationID) bool { return other == id })
	return true
}

// ListConfigurableProviders returns detached directory entries in registration order.
func (catalog *Registry) ListConfigurableProviders() []ConfigurableProvider {
	result := make([]ConfigurableProvider, 0)
	for _, id := range catalog.directoryOrder {
		for _, entry := range catalog.directories[id] {
			entry.SettingsPath = slices.Clone(entry.SettingsPath)
			entry.Declared = cloneBool(entry.Declared)
			result = append(result, entry)
		}
	}
	return result
}
