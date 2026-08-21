package llm

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

// directoryRegistrationState owns one replaceable configurable-provider
// entry independently of executable adapter routes.
type directoryRegistrationState struct {
	owner    *Runtime
	held     []ConfigurableProvider
	disposed bool
}

type directoryHandle struct {
	state *directoryRegistrationState
}

func (owner *Runtime) RegisterConfigurableProviders(
	requestContext context.Context,
	entries []ConfigurableProvider,
) (DirectoryRegistrationHandle, error) {
	if len(entries) == 0 {
		return nil, MustLlmError("a configurable-provider registration must declare at least one provider", "INVALID_DIRECTORY")
	}
	candidates, err := validateDirectoryEntries(entries)
	if err != nil {
		return nil, err
	}
	state := &directoryRegistrationState{
		owner: owner,
	}
	if err := owner.commitDirectory(state, candidates); err != nil {
		return nil, err
	}
	if err := owner.publishUpdate(requestContext); err != nil {
		return nil, errors.Join(err, state.releaseOwned(requestContext))
	}
	return &directoryHandle{
		state: state,
	}, nil
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

func (owner *Runtime) commitDirectory(state *directoryRegistrationState, candidates []ConfigurableProvider) error {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.closed {
		return MustLlmError("the LLM Runtime is closed", "REGISTRATION_DISPOSED")
	}
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
	if handleState == nil || handleState.state == nil {
		return nil
	}
	return handleState.state.releaseOwned(closeContext)
}

func (owner *Runtime) ListConfigurableProviders() []ConfigurableProvider {
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

func cloneBool(source *bool) *bool {
	if source == nil {
		return nil
	}
	value := *source
	return &value
}
