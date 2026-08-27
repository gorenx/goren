package systemprompt

import (
	"fmt"
	"sort"
	"strings"

	"github.com/gorenx/goren/agentmessage"
)

const contextSnapshotPrefix = "Current runtime context. This snapshot supersedes earlier runtime-context snapshots."

// RenderPrompt interpolates strict variable references, drops empty sections,
// and joins the rest with one blank line.
func RenderPrompt(assembled PromptAssembly) (string, error) {
	parts := make([]string, 0, len(assembled.Sections))
	for _, entry := range assembled.Sections {
		resolved, err := interpolate(entry.Name, entry.Text, "section", assembled.Variables)
		if err != nil {
			return "", err
		}
		if resolved != "" {
			parts = append(parts, resolved)
		}
	}
	return strings.Join(parts, "\n\n"), nil
}

// RenderContextSections interpolates and attributes every non-empty context.
func RenderContextSections(assembled PromptAssembly) ([]agentmessage.ContextSnapshotSection, error) {
	resolvedSections := make([]agentmessage.ContextSnapshotSection, 0, len(assembled.Contexts))
	for _, entry := range assembled.Contexts {
		resolved, err := interpolate(entry.Name, entry.Text, "context", assembled.Variables)
		if err != nil {
			return nil, err
		}
		if resolved != "" {
			resolvedSections = append(resolvedSections, agentmessage.ContextSnapshotSection{Name: entry.Name, Text: resolved})
		}
	}
	return resolvedSections, nil
}

// JoinContextSections renders the canonical superseding-snapshot prefix and
// joins already-rendered context sections.
func JoinContextSections(sections []agentmessage.ContextSnapshotSection) string {
	parts := make([]string, len(sections))
	for index, entry := range sections {
		parts[index] = entry.Text
	}
	body := strings.Join(parts, "\n\n")
	if body == "" {
		return ""
	}
	return contextSnapshotPrefix + "\n\n" + body
}

// RenderContextSnapshot resolves and joins the complete current context.
func RenderContextSnapshot(assembled PromptAssembly) (string, error) {
	resolvedSections, err := RenderContextSections(assembled)
	if err != nil {
		return "", err
	}
	return JoinContextSections(resolvedSections), nil
}

func interpolate(name string, input string, kind string, variables map[string]VariableValue) (string, error) {
	var output strings.Builder
	last := 0
	for {
		relativeOpen := strings.Index(input[last:], "{{")
		if relativeOpen < 0 {
			break
		}
		open := last + relativeOpen
		groupEnd, referenceName, matched := referenceAt(input[open:])
		if !matched {
			if strings.Contains(input[open+2:], "}}") {
				fragment := input[open:]
				if len(fragment) > 16 {
					fragment = fragment[:16]
				}
				return "", fmt.Errorf("systemprompt: malformed prompt variable reference at %q in %s %q (references are complete simple {{name}} groups)", fragment+"…", kind, name)
			}
			output.WriteString(input[last : open+2])
			last = open + 2
			continue
		}
		if !variableNamePattern.MatchString(referenceName) {
			return "", fmt.Errorf("systemprompt: malformed prompt variable reference %q in %s %q (variable names match %s)", "{{"+referenceName+"}}", kind, name, variableNamePattern.String())
		}
		retained, exists := variables[referenceName]
		if !exists {
			known := make([]string, 0, len(variables))
			for knownName := range variables {
				known = append(known, knownName)
			}
			sort.Strings(known)
			knownLabel := "(none)"
			if len(known) > 0 {
				knownLabel = strings.Join(known, ", ")
			}
			return "", fmt.Errorf("systemprompt: unknown prompt variable %q in %s %q; registered variables: %s", "{{"+referenceName+"}}", kind, name, knownLabel)
		}
		if !retained.Defined {
			return "", fmt.Errorf("systemprompt: prompt variable %q has no value for this assembly (%s %q)", "{{"+referenceName+"}}", kind, name)
		}
		output.WriteString(input[last:open])
		output.WriteString(retained.Value)
		last = open + groupEnd
	}
	output.WriteString(input[last:])
	return output.String(), nil
}

func referenceAt(input string) (int, string, bool) {
	if !strings.HasPrefix(input, "{{") {
		return 0, "", false
	}
	closing := strings.Index(input[2:], "}}")
	if closing < 0 {
		return 0, "", false
	}
	closing += 2
	inner := input[2:closing]
	if strings.ContainsAny(inner, "{}") {
		return 0, "", false
	}
	if closing+2 > len(input) {
		return 0, "", false
	}
	return closing + 2, inner, true
}
