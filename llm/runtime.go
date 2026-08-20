package llm

import (
	"context"
	"fmt"
	"slices"
)

type resolvedCall struct {
	config  CallConfig
	context *ModelContext
}

func (owner *Runtime) RetryPolicyFor(providerRoute string) (RetryPolicy, error) {
	record, err := owner.routeFor(providerRoute)
	if err != nil {
		return nil, err
	}
	return record.policy.CloneRetryPolicy(), nil
}

func (owner *Runtime) ListModels(requestContext context.Context, providerRoute string) ([]ModelInfo, error) {
	record, err := owner.routeFor(providerRoute)
	if err != nil {
		return nil, err
	}
	catalog, ok := record.backend.(ModelCatalog)
	if !ok {
		return []ModelInfo{}, nil
	}
	models, err := catalog.ListModels(requestContext, providerRoute)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(models))
	result := make([]ModelInfo, 0, len(models))
	for _, candidate := range models {
		if candidate.Provider != providerRoute || candidate.ID == "" || candidate.Name == "" {
			return nil, MustLlmError(fmt.Sprintf("adapter returned invalid model metadata for provider %q", providerRoute), "INVALID_CATALOG")
		}
		if _, exists := seen[candidate.ID]; exists {
			return nil, MustLlmError(fmt.Sprintf("adapter returned duplicate model metadata for provider %q", providerRoute), "INVALID_CATALOG")
		}
		seen[candidate.ID] = struct{}{}
		candidate.InputModalities = slices.Clone(candidate.InputModalities)
		result = append(result, candidate)
	}
	return result, nil
}

func (owner *Runtime) ResolveModelInfo(
	requestContext context.Context,
	providerRoute string,
	modelID string,
) (ResolvedModelInfo, error) {
	record, err := owner.routeFor(providerRoute)
	if err != nil {
		return ResolvedModelInfo{}, err
	}
	return owner.resolveModelFor(requestContext, record, modelID)
}

func (owner *Runtime) resolveModelFor(
	requestContext context.Context,
	record *adapterRoute,
	modelID string,
) (ResolvedModelInfo, error) {
	providerRoute := record.metadata.ID
	resolved := ResolvedModelInfo{ModelInfo: ModelInfo{Provider: providerRoute, ID: modelID, Name: modelID}}
	if resolver, ok := record.backend.(ModelResolver); ok {
		candidate, err := resolver.ResolveModel(requestContext, providerRoute, modelID)
		if err != nil {
			return ResolvedModelInfo{}, err
		}
		resolved = candidate
	}
	if resolved.Provider != providerRoute || resolved.ID != modelID || resolved.Name == "" {
		return ResolvedModelInfo{}, MustLlmError(
			fmt.Sprintf("adapter returned invalid exact model metadata for provider %q model %q", providerRoute, modelID),
			"INVALID_MODEL_INFO",
		)
	}
	resolved.InputModalities = slices.Clone(resolved.InputModalities)
	if resolved.Context != nil {
		if resolved.Context.ContextWindow <= 0 {
			return ResolvedModelInfo{}, MustLlmError(
				fmt.Sprintf("adapter returned invalid context metadata for provider %q model %q", providerRoute, modelID),
				"INVALID_MODEL_CONTEXT",
			)
		}
		contextCopy := *resolved.Context
		resolved.Context = &contextCopy
	}
	if resolved.DefaultMaxTokens != nil {
		if *resolved.DefaultMaxTokens <= 0 {
			return ResolvedModelInfo{}, MustLlmError(
				fmt.Sprintf("adapter returned invalid default maxTokens for provider %q model %q", providerRoute, modelID),
				"INVALID_MODEL_MAX_TOKENS",
			)
		}
		resolved.DefaultMaxTokens = cloneInt(resolved.DefaultMaxTokens)
	}
	if resolved.Reasoning == nil {
		return resolved, nil
	}
	if len(resolved.Reasoning.Efforts) == 0 {
		return ResolvedModelInfo{}, MustLlmError(
			fmt.Sprintf("adapter returned invalid reasoning metadata for provider %q model %q", providerRoute, modelID),
			"INVALID_MODEL_REASONING",
		)
	}
	seen := make(map[ReasoningEffortID]struct{}, len(resolved.Reasoning.Efforts))
	efforts := make([]ReasoningEffortInfo, 0, len(resolved.Reasoning.Efforts))
	for _, effort := range resolved.Reasoning.Efforts {
		if effort.ID == "" || effort.Name == "" {
			return ResolvedModelInfo{}, MustLlmError(
				fmt.Sprintf("adapter returned invalid reasoning metadata for provider %q model %q", providerRoute, modelID),
				"INVALID_MODEL_REASONING",
			)
		}
		if _, exists := seen[effort.ID]; exists {
			return ResolvedModelInfo{}, MustLlmError(
				fmt.Sprintf("adapter returned duplicate reasoning metadata for provider %q model %q", providerRoute, modelID),
				"INVALID_MODEL_REASONING",
			)
		}
		seen[effort.ID] = struct{}{}
		efforts = append(efforts, effort)
	}
	if resolved.Reasoning.DefaultEffort != "" {
		if _, exists := seen[resolved.Reasoning.DefaultEffort]; !exists {
			return ResolvedModelInfo{}, MustLlmError(
				fmt.Sprintf("adapter returned an unknown default reasoning effort for provider %q model %q", providerRoute, modelID),
				"INVALID_MODEL_REASONING",
			)
		}
	}
	reasoningCopy := *resolved.Reasoning
	reasoningCopy.Efforts = efforts
	resolved.Reasoning = &reasoningCopy
	return resolved, nil
}

func (owner *Runtime) ResolveCallConfig(requestContext context.Context, proposed CallConfig) (CallConfig, error) {
	record, err := owner.routeFor(proposed.Provider)
	if err != nil {
		return CallConfig{}, err
	}
	resolved, err := owner.resolveCallFor(requestContext, record, proposed)
	if err != nil {
		return CallConfig{}, err
	}
	return cloneCallConfig(resolved.config), nil
}

func (owner *Runtime) resolveCallFor(
	requestContext context.Context,
	record *adapterRoute,
	proposed CallConfig,
) (resolvedCall, error) {
	if proposed.Provider == "" || proposed.Model == "" {
		return resolvedCall{}, MustLlmError("an LLM call needs a provider and model", "INVALID_ARGS")
	}
	metadata, err := owner.resolveModelFor(requestContext, record, proposed.Model)
	if err != nil {
		return resolvedCall{}, err
	}
	effective := cloneCallConfig(proposed)
	if effective.MaxTokens == nil && metadata.DefaultMaxTokens != nil {
		effective.MaxTokens = cloneInt(metadata.DefaultMaxTokens)
	}
	if metadata.Reasoning == nil {
		if effective.ReasoningEffort != "" {
			return resolvedCall{}, MustLlmError(
				fmt.Sprintf("provider %q model %q does not support reasoning effort %q", proposed.Provider, proposed.Model, effective.ReasoningEffort),
				"UNSUPPORTED_REASONING_EFFORT",
			)
		}
	} else {
		effortID := effective.ReasoningEffort
		if effortID == "" {
			effortID = metadata.Reasoning.DefaultEffort
		}
		if effortID != "" {
			matched := slices.ContainsFunc(metadata.Reasoning.Efforts, func(candidate ReasoningEffortInfo) bool {
				return candidate.ID == effortID
			})
			if !matched {
				return resolvedCall{}, MustLlmError(
					fmt.Sprintf("provider %q model %q does not support reasoning effort %q", proposed.Provider, proposed.Model, effortID),
					"UNSUPPORTED_REASONING_EFFORT",
				)
			}
			effective.ReasoningEffort = effortID
		}
	}
	result := resolvedCall{config: effective}
	if metadata.Context != nil {
		contextCopy := *metadata.Context
		result.context = &contextCopy
	}
	return result, nil
}

func cloneModelContext(source *ModelContext) *ModelContext {
	if source == nil {
		return nil
	}
	detached := *source
	return &detached
}
