package systemprompt

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
)

// promptAssembler resolves one immutable provider-membership snapshot. It
// owns assembly ordering and post-waterfall invariants, not registry mutation.
type promptAssembler struct {
	sourceScope *plugin.Scope
	store       *promptStore
	toolOrder   []string
}

func newPromptAssembler(sourceScope *plugin.Scope, store *promptStore, toolOrder []string) promptAssembler {
	return promptAssembler{sourceScope: sourceScope, store: store, toolOrder: slices.Clone(toolOrder)}
}

func (assembler promptAssembler) assemble(requestContext context.Context, assemblyContext AssembleContext) (PromptAssembly, error) {
	state := assembler.store.capture(assemblyContext.Scope)
	variables := make(map[string]VariableValue)
	for _, providerLayer := range state.variableProviders {
		for _, item := range providerLayer {
			resolved, err := item.retained(requestContext, assemblyContext)
			if err != nil {
				return PromptAssembly{}, fmt.Errorf("systemprompt: resolve prompt variable %q: %w", item.name, err)
			}
			variables[item.name] = resolved
		}
	}

	assembledSections := make([]AssembledSection, 0, len(state.sections))
	completeDefinitions := make([]PromptSection, 0, 1)
	for _, definition := range state.sections {
		if definition.Complete {
			completeDefinitions = append(completeDefinitions, definition)
		}
	}
	if len(completeDefinitions) > 1 {
		names := make([]string, len(completeDefinitions))
		for index, definition := range completeDefinitions {
			names[index] = fmt.Sprintf("%q", definition.Name)
		}
		return PromptAssembly{}, fmt.Errorf("systemprompt: multiple complete prompt sections are active: %s", strings.Join(names, ", "))
	}
	var completeSection *AssembledSection
	for _, definition := range state.sections {
		resolvedText, err := definition.Text.ResolveText(requestContext, assemblyContext)
		if err != nil {
			return PromptAssembly{}, fmt.Errorf("systemprompt: resolve prompt section %q: %w", definition.Name, err)
		}
		assembledEntry := AssembledSection{Name: definition.Name, Text: resolvedText}
		assembledSections = append(assembledSections, assembledEntry)
		if definition.Complete {
			retained := assembledEntry
			completeSection = &retained
		}
	}

	assembledContexts := make([]AssembledContext, 0, len(state.contexts))
	if !state.contextSuppressed {
		for _, definition := range state.contexts {
			resolvedText, err := definition.Text.ResolveText(requestContext, assemblyContext)
			if err != nil {
				return PromptAssembly{}, fmt.Errorf("systemprompt: resolve prompt context %q: %w", definition.Name, err)
			}
			assembledContexts = append(assembledContexts, AssembledContext{Name: definition.Name, Text: resolvedText})
		}
	}

	collectedSchemas := make([]llm.ToolSchema, 0)
	knownNames := make(map[string]struct{})
	for _, callback := range state.toolProviders {
		providerResult, err := callback(requestContext, assemblyContext)
		if err != nil {
			return PromptAssembly{}, fmt.Errorf("systemprompt: resolve tool provider: %w", err)
		}
		for _, schema := range providerResult.Schemas {
			detached, detachErr := detachToolSchema(schema)
			if detachErr != nil {
				return PromptAssembly{}, detachErr
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
	orderedSchemas, err := orderToolSchemas(collectedSchemas, assembler.toolOrder, knownNames)
	if err != nil {
		return PromptAssembly{}, err
	}

	assembled := PromptAssembly{
		Sections: assembledSections, Contexts: assembledContexts, Tools: orderedSchemas, Variables: variables,
	}
	payload := assemblePayload{assembled: &assembled, assemblyContext: assemblyContext}
	transformed, err := plugin.WaterfallScopedFrom(requestContext, assembler.sourceScope, assemblyContext.Scope,
		assembleEvent, payload, func(context.Context, assemblePayload) (PromptAssembly, error) {
			return cloneAssembly(assembled), nil
		})
	if err != nil {
		return PromptAssembly{}, err
	}
	if err := validateAssembly(transformed); err != nil {
		return PromptAssembly{}, err
	}
	transformed = cloneAssembly(transformed)
	if completeSection != nil {
		transformed.Sections = []AssembledSection{*completeSection}
	}
	if state.contextSuppressed {
		transformed.Contexts = []AssembledContext{}
	}
	return transformed, nil
}

func validateAssembly(assembled PromptAssembly) error {
	sectionNames := make(map[string]struct{}, len(assembled.Sections))
	for _, entry := range assembled.Sections {
		if entry.Name == "" {
			return errors.New("systemprompt: assembled section names must be non-empty")
		}
		if _, exists := sectionNames[entry.Name]; exists {
			return fmt.Errorf("systemprompt: assembled section name %q is duplicated", entry.Name)
		}
		sectionNames[entry.Name] = struct{}{}
	}
	contextNames := make(map[string]struct{}, len(assembled.Contexts))
	for _, entry := range assembled.Contexts {
		if entry.Name == "" {
			return errors.New("systemprompt: assembled context names must be non-empty")
		}
		if _, exists := contextNames[entry.Name]; exists {
			return fmt.Errorf("systemprompt: assembled context name %q is duplicated", entry.Name)
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
			return fmt.Errorf("systemprompt: assembled variable name %q is invalid", name)
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
			Name: schema.Name, Description: schema.Description, Parameters: slices.Clone(schema.Parameters),
		}
	}
	for name, retained := range assembled.Variables {
		detached.Variables[name] = retained
	}
	return detached
}
