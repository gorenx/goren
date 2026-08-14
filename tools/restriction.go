package tools

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

func compileRestriction(restriction ToolRestriction, knownNames map[string]struct{}) (compiledRestriction, error) {
	compiled := compiledRestriction{}
	if restriction.Allow != nil {
		compiled.allow = make(map[string]struct{}, len(restriction.Allow))
		if err := retainRestrictedNames(compiled.allow, restriction.Allow, knownNames); err != nil {
			return compiledRestriction{}, err
		}
	}
	if restriction.Deny != nil {
		compiled.deny = make(map[string]struct{}, len(restriction.Deny))
		if err := retainRestrictedNames(compiled.deny, restriction.Deny, knownNames); err != nil {
			return compiledRestriction{}, err
		}
	}
	return compiled, nil
}

func retainRestrictedNames(destination map[string]struct{}, candidates []string, knownNames map[string]struct{}) error {
	unknown := make([]string, 0)
	for _, name := range candidates {
		if strings.TrimSpace(name) == "" || name != strings.TrimSpace(name) {
			return errors.New("tools: restriction names must be non-empty and trimmed")
		}
		if name == RunCodeName {
			return fmt.Errorf("tools: restriction cannot name reserved transport %q", RunCodeName)
		}
		if _, duplicate := destination[name]; duplicate {
			return fmt.Errorf("tools: restriction names tool %q more than once", name)
		}
		destination[name] = struct{}{}
		if _, found := knownNames[name]; !found {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		slices.Sort(unknown)
		return fmt.Errorf("tools: restriction names unknown global tools %s; known global tools: %s",
			strings.Join(unknown, ", "), strings.Join(sortedNames(knownNames), ", "))
	}
	return nil
}
