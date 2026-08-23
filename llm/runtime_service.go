package llm

import (
	"context"
	"errors"
	"sync"

	"github.com/gorenx/goren/plugin"
)

type Runtime struct {
	plugin.Base
	reporter ObserverFailureReporter

	mu             sync.RWMutex
	routes         map[string]*adapterRoute
	routeOrder     []string
	directory      map[string]ConfigurableProvider
	directoryOrder []string
	discoveries    map[string]*discoveryEntry
	closed         bool
}

// NewRuntime creates the provider-neutral LLM Service Plugin.
func NewRuntime(reporter ObserverFailureReporter) *Runtime {
	return &Runtime{
		reporter:    reporter,
		routes:      make(map[string]*adapterRoute),
		directory:   make(map[string]ConfigurableProvider),
		discoveries: make(map[string]*discoveryEntry),
	}
}

// Manifest declares the canonical LLM Service binding.
func (owner *Runtime) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: PluginName,
		Provides: []plugin.ProvidedService{
			plugin.NewProvidedService[LlmRuntime](owner),
		},
	}
}

// Apply validates startup cancellation before publication.
func (*Runtime) Apply(requestContext context.Context) error {
	return requestContext.Err()
}

// Dispose closes registration mutation and releases retained references.
func (owner *Runtime) Dispose(context.Context) error {
	owner.mu.Lock()
	owner.closed = true
	owner.routes = make(map[string]*adapterRoute)
	owner.routeOrder = nil
	owner.directory = make(map[string]ConfigurableProvider)
	owner.directoryOrder = nil
	owner.discoveries = make(map[string]*discoveryEntry)
	owner.mu.Unlock()
	return nil
}

func (owner *Runtime) ListProviders() []ProviderInfo {
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

func (owner *Runtime) publishUpdate(requestContext context.Context) error {
	dispatchErr := plugin.Publish(requestContext, owner, AdaptersUpdated{})
	if dispatchErr == nil {
		return nil
	}
	if errorTreeHasCode(dispatchErr, "INVARIANT") {
		return dispatchErr
	}
	owner.reportContained(dispatchErr)
	return nil
}

func (owner *Runtime) reportContained(dispatchErr error) {
	if owner.reporter == nil {
		return
	}
	defer func() {
		_ = recover()
	}()
	owner.reporter.ReportObserverFailure(dispatchErr)
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
