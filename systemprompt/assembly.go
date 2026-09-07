package systemprompt

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/gorenx/goren/llm"
)

// assemblyResolver evaluates providers from a fixed registration snapshot.
type assemblyResolver struct {
	requestContext  context.Context
	assemblyContext AssembleContext
	layers          []promptLayerSnapshot
	toolOrder       []string
}

type sectionResolution struct {
	entries         []AssembledSection
	complete        AssembledSection
	completePresent bool
}

func (resolver *assemblyResolver) resolve() (PromptAssembly, error) {
	variables, err := resolver.resolveVariables()
	if err != nil {
		return PromptAssembly{}, err
	}
	sections, err := resolver.resolveSections()
	if err != nil {
		return PromptAssembly{}, err
	}
	contexts, err := resolver.resolveContexts()
	if err != nil {
		return PromptAssembly{}, err
	}
	toolSchemas, err := resolver.resolveTools()
	if err != nil {
		return PromptAssembly{}, err
	}
	assembled := PromptAssembly{
		Sections:  sections.entries,
		Contexts:  contexts,
		Tools:     toolSchemas,
		Variables: variables,
	}
	if err := validateAssembly(assembled); err != nil {
		return PromptAssembly{}, err
	}
	if sections.completePresent {
		assembled.Sections = []AssembledSection{sections.complete}
	}
	return assembled, nil
}

func (resolver *assemblyResolver) resolveVariables() (
	map[string]VariableValue,
	error,
) {
	variables := make(map[string]VariableValue)
	for _, layer := range resolver.layers {
		for _, entry := range layer.variables {
			resolved, err := entry.provider.ResolveVariable(
				resolver.requestContext,
				resolver.assemblyContext,
			)
			if err != nil {
				return nil, fmt.Errorf(
					"systemprompt: resolve prompt variable %q: %w",
					entry.name,
					err,
				)
			}
			variables[entry.name] = resolved
		}
	}
	return variables, nil
}

func (resolver *assemblyResolver) resolveSections() (
	sectionResolution,
	error,
) {
	sections := mergeSections(resolver.layers)
	completeDefinitions := make([]PromptSection, 0, 1)
	for _, definition := range sections {
		if definition.Complete {
			completeDefinitions = append(completeDefinitions, definition)
		}
	}
	if len(completeDefinitions) > 1 {
		names := make([]string, len(completeDefinitions))
		for index, definition := range completeDefinitions {
			names[index] = fmt.Sprintf("%q", definition.Name)
		}
		return sectionResolution{}, fmt.Errorf(
			"systemprompt: multiple complete prompt sections are active: %s",
			strings.Join(names, ", "),
		)
	}
	assembledSections := make([]AssembledSection, 0, len(sections))
	resolved := sectionResolution{
		entries: assembledSections,
	}
	for _, definition := range sections {
		resolvedText, err := definition.Text.ResolveText(
			resolver.requestContext,
			resolver.assemblyContext,
		)
		if err != nil {
			return sectionResolution{}, fmt.Errorf(
				"systemprompt: resolve prompt section %q: %w",
				definition.Name,
				err,
			)
		}
		assembledEntry := AssembledSection{
			Name: definition.Name,
			Text: resolvedText,
		}
		resolved.entries = append(resolved.entries, assembledEntry)
		if definition.Complete {
			resolved.complete = assembledEntry
			resolved.completePresent = true
		}
	}
	return resolved, nil
}

func (resolver *assemblyResolver) resolveContexts() ([]AssembledContext, error) {
	// A suppressor in any captured layer disables all runtime-context sections.
	// Skip their providers entirely; system sections and tools are unaffected.
	if contextSuppressed(resolver.layers) {
		return []AssembledContext{}, nil
	}
	contexts := mergeContexts(resolver.layers)
	resolved := make([]AssembledContext, 0, len(contexts))
	for _, definition := range contexts {
		resolvedText, err := definition.Text.ResolveText(
			resolver.requestContext,
			resolver.assemblyContext,
		)
		if err != nil {
			return nil, fmt.Errorf(
				"systemprompt: resolve prompt context %q: %w",
				definition.Name,
				err,
			)
		}
		resolved = append(resolved, AssembledContext{
			Name: definition.Name,
			Text: resolvedText,
		})
	}
	return resolved, nil
}

func (resolver *assemblyResolver) resolveTools() ([]llm.ToolSchema, error) {
	collectedSchemas := make([]llm.ToolSchema, 0)
	knownNames := make(map[string]struct{})
	definitions := resolver.assemblyContext.Tools
	for _, schema := range definitions.Schemas {
		detached, err := detachToolSchema(schema)
		if err != nil {
			return nil, err
		}
		collectedSchemas = append(collectedSchemas, detached)
	}
	if definitions.KnownNames == nil {
		for _, schema := range definitions.Schemas {
			knownNames[schema.Name] = struct{}{}
		}
	} else {
		for _, name := range definitions.KnownNames {
			knownNames[name] = struct{}{}
		}
	}
	orderedSchemas, err := orderToolSchemas(
		collectedSchemas,
		resolver.toolOrder,
		knownNames,
	)
	if err != nil {
		return nil, err
	}
	return orderedSchemas, nil
}

func mergeSections(layers []promptLayerSnapshot) []PromptSection {
	names := make([]string, 0)
	byName := make(map[string]PromptSection)
	for _, layer := range layers {
		for _, definition := range layer.sections {
			if _, exists := byName[definition.Name]; !exists {
				names = append(names, definition.Name)
			}
			byName[definition.Name] = definition
		}
	}
	merged := make([]PromptSection, 0, len(names))
	for _, name := range names {
		merged = append(merged, byName[name])
	}
	sort.SliceStable(merged, func(leftIndex int, rightIndex int) bool {
		return merged[leftIndex].Order < merged[rightIndex].Order
	})
	return merged
}

func mergeContexts(layers []promptLayerSnapshot) []PromptContext {
	names := make([]string, 0)
	byName := make(map[string]PromptContext)
	for _, layer := range layers {
		for _, definition := range layer.contexts {
			if _, exists := byName[definition.Name]; !exists {
				names = append(names, definition.Name)
			}
			byName[definition.Name] = definition
		}
	}
	merged := make([]PromptContext, 0, len(names))
	for _, name := range names {
		merged = append(merged, byName[name])
	}
	sort.SliceStable(merged, func(leftIndex int, rightIndex int) bool {
		return merged[leftIndex].Order < merged[rightIndex].Order
	})
	return merged
}

func contextSuppressed(layers []promptLayerSnapshot) bool {
	for _, layer := range layers {
		if layer.contextSuppressed {
			return true
		}
	}
	return false
}

func validateAssembly(assembled PromptAssembly) error {
	sectionNames := make(map[string]struct{}, len(assembled.Sections))
	for _, entry := range assembled.Sections {
		if entry.Name == "" {
			return errors.New("systemprompt: assembled section names must be non-empty")
		}
		if _, exists := sectionNames[entry.Name]; exists {
			return fmt.Errorf(
				"systemprompt: assembled section name %q is duplicated",
				entry.Name,
			)
		}
		sectionNames[entry.Name] = struct{}{}
	}
	contextNames := make(map[string]struct{}, len(assembled.Contexts))
	for _, entry := range assembled.Contexts {
		if entry.Name == "" {
			return errors.New("systemprompt: assembled context names must be non-empty")
		}
		if _, exists := contextNames[entry.Name]; exists {
			return fmt.Errorf(
				"systemprompt: assembled context name %q is duplicated",
				entry.Name,
			)
		}
		contextNames[entry.Name] = struct{}{}
	}
	for _, schema := range assembled.Tools {
		if schema.Name == "" {
			return errors.New("systemprompt: assembled tool names must be non-empty")
		}
	}
	for name := range assembled.Variables {
		if !variableNamePattern.MatchString(name) {
			return fmt.Errorf(
				"systemprompt: assembled variable name %q is invalid",
				name,
			)
		}
	}
	return nil
}
