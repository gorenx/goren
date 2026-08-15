package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/gorenx/goren/plugin"
)

type discoveryEntry struct {
	backend ModelDiscovery
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
