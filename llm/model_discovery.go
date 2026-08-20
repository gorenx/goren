package llm

import (
	"context"
	"fmt"
	"strings"
)

type discoveryEntry struct {
	backend ModelDiscovery
}

type discoveryRegistration struct {
	owner      *Runtime
	settingsNS string
	record     *discoveryEntry
	disposed   bool
}

func (owner *Runtime) RegisterModelDiscovery(
	settingsNS string,
	backend ModelDiscovery,
) (ModelDiscoveryRegistration, error) {
	if strings.TrimSpace(settingsNS) == "" {
		return nil, MustLlmError("model discovery needs a non-empty settings namespace", "INVALID_DISCOVERY")
	}
	if backend == nil {
		return nil, MustLlmError("model discovery callback is nil", "INVALID_DISCOVERY")
	}
	record := &discoveryEntry{
		backend: backend,
	}
	owner.mu.Lock()
	if owner.closed {
		owner.mu.Unlock()
		return nil, MustLlmError("the LLM Runtime is closed", "REGISTRATION_DISPOSED")
	}
	if owner.discoveries[settingsNS] != nil {
		owner.mu.Unlock()
		return nil, MustLlmError(fmt.Sprintf("model discovery for %q is already registered", settingsNS), "DUPLICATE_DISCOVERY")
	}
	owner.discoveries[settingsNS] = record
	owner.mu.Unlock()
	return &discoveryRegistration{
		owner:      owner,
		settingsNS: settingsNS,
		record:     record,
	}, nil
}

func (registration *discoveryRegistration) Release(context.Context) error {
	if registration == nil || registration.owner == nil {
		return nil
	}
	registration.owner.mu.Lock()
	if registration.disposed {
		registration.owner.mu.Unlock()
		return nil
	}
	registration.disposed = true
	if registration.owner.discoveries[registration.settingsNS] == registration.record {
		delete(registration.owner.discoveries, registration.settingsNS)
	}
	registration.owner.mu.Unlock()
	return nil
}

func (owner *Runtime) DiscoverModels(
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
