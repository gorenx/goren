package llm

import (
	"fmt"
	"slices"
)

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

func cloneBool(source *bool) *bool {
	if source == nil {
		return nil
	}
	value := *source
	return &value
}
