package systemprompt

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/gorenx/goren/llm"
)

type assemblyAction struct {
	assembled PromptAssembly
}

func (action *assemblyAction) Execute(
	context.Context,
	AssembleRequest,
) (PromptAssembly, error) {
	return cloneAssembly(action.assembled), nil
}

func assembleLayers(
	requestContext context.Context,
	assemblyContext AssembleContext,
	layers []promptLayerSnapshot,
	toolOrder []string,
) (PromptAssembly, *AssembledSection, bool, error) {
	variables := make(map[string]VariableValue)
	for _, layer := range layers {
		for _, entry := range layer.variables {
			resolved, err := entry.provider.ResolveVariable(
				requestContext,
				assemblyContext,
			)
			if err != nil {
				return PromptAssembly{}, nil, false, fmt.Errorf(
					"systemprompt: resolve prompt variable %q: %w",
					entry.name,
					err,
				)
			}
			variables[entry.name] = resolved
		}
	}

	sections := mergeSections(layers)
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
		return PromptAssembly{}, nil, false, fmt.Errorf(
			"systemprompt: multiple complete prompt sections are active: %s",
			strings.Join(names, ", "),
		)
	}
	assembledSections := make([]AssembledSection, 0, len(sections))
	var completeSection *AssembledSection
	for _, definition := range sections {
		resolvedText, err := definition.Text.ResolveText(
			requestContext,
			assemblyContext,
		)
		if err != nil {
			return PromptAssembly{}, nil, false, fmt.Errorf(
				"systemprompt: resolve prompt section %q: %w",
				definition.Name,
				err,
			)
		}
		assembledEntry := AssembledSection{
			Name: definition.Name,
			Text: resolvedText,
		}
		assembledSections = append(assembledSections, assembledEntry)
		if definition.Complete {
			retained := assembledEntry
			completeSection = &retained
		}
	}

	suppressed := contextSuppressed(layers)
	assembledContexts := make([]AssembledContext, 0)
	if !suppressed {
		contexts := mergeContexts(layers)
		assembledContexts = make([]AssembledContext, 0, len(contexts))
		for _, definition := range contexts {
			resolvedText, err := definition.Text.ResolveText(
				requestContext,
				assemblyContext,
			)
			if err != nil {
				return PromptAssembly{}, nil, false, fmt.Errorf(
					"systemprompt: resolve prompt context %q: %w",
					definition.Name,
					err,
				)
			}
			assembledContexts = append(assembledContexts, AssembledContext{
				Name: definition.Name,
				Text: resolvedText,
			})
		}
	}

	collectedSchemas := make([]llm.ToolSchema, 0)
	knownNames := make(map[string]struct{})
	for _, entry := range mergeToolProviders(layers) {
		providerResult, err := entry.provider.ResolveTools(
			requestContext,
			assemblyContext,
		)
		if err != nil {
			return PromptAssembly{}, nil, false, fmt.Errorf(
				"systemprompt: resolve tool provider %q: %w",
				entry.name,
				err,
			)
		}
		for _, schema := range providerResult.Schemas {
			detached, detachErr := detachToolSchema(schema)
			if detachErr != nil {
				return PromptAssembly{}, nil, false, detachErr
			}
			collectedSchemas = append(collectedSchemas, detached)
		}
		acceptedNames := providerResult.KnownNames
		if acceptedNames == nil {
			acceptedNames = make([]string, len(providerResult.Schemas))
			for index, schema := range providerResult.Schemas {
				acceptedNames[index] = schema.Name
			}
		}
		for _, name := range acceptedNames {
			knownNames[name] = struct{}{}
		}
	}
	orderedSchemas, err := orderToolSchemas(
		collectedSchemas,
		toolOrder,
		knownNames,
	)
	if err != nil {
		return PromptAssembly{}, nil, false, err
	}

	return PromptAssembly{
		Sections:  assembledSections,
		Contexts:  assembledContexts,
		Tools:     orderedSchemas,
		Variables: variables,
	}, completeSection, suppressed, nil
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

func mergeToolProviders(layers []promptLayerSnapshot) []namedToolProvider {
	names := make([]string, 0)
	byName := make(map[string]ToolProvider)
	for _, layer := range layers {
		for _, entry := range layer.toolProviders {
			if _, exists := byName[entry.name]; !exists {
				names = append(names, entry.name)
			}
			byName[entry.name] = entry.provider
		}
	}
	merged := make([]namedToolProvider, 0, len(names))
	for _, name := range names {
		merged = append(merged, namedToolProvider{
			name:     name,
			provider: byName[name],
		})
	}
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

func cloneAssembly(assembled PromptAssembly) PromptAssembly {
	detached := PromptAssembly{
		Sections:  slices.Clone(assembled.Sections),
		Contexts:  slices.Clone(assembled.Contexts),
		Tools:     make([]llm.ToolSchema, len(assembled.Tools)),
		Variables: make(map[string]VariableValue, len(assembled.Variables)),
	}
	for index, schema := range assembled.Tools {
		detached.Tools[index] = llm.ToolSchema{
			Name:        schema.Name,
			Description: schema.Description,
			Parameters:  slices.Clone(schema.Parameters),
		}
	}
	for name, retained := range assembled.Variables {
		detached.Variables[name] = retained
	}
	return detached
}
