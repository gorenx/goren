package llm

import (
	"context"
	"errors"
	"sync"

	"github.com/gorenx/goren/plugin"
)

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
