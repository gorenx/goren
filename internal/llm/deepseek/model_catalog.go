package deepseek

import (
	"context"
	"errors"

	"github.com/gorenx/goren/llm"
)

func (backend *Adapter) DescribeProvider(providerRoute string) (llm.ProviderInfo, error) {
	return llm.ProviderInfo{
		ID:   providerRoute,
		Name: "DeepSeek",
	}, nil
}

func (backend *Adapter) ProviderRetryPolicy(string) (llm.RetryPolicy, error) {
	connection, err := backend.connections.CurrentConnection()
	if err != nil {
		return nil, err
	}
	if connection.RetryPolicy == nil {
		return nil, errors.New("llm-deepseek: resolved retry policy is nil")
	}
	return connection.RetryPolicy.CloneRetryPolicy(), nil
}

func (backend *Adapter) ListModels(requestContext context.Context, providerRoute string) ([]llm.ModelInfo, error) {
	if err := requestContext.Err(); err != nil {
		return nil, err
	}
	connection, err := backend.connections.CurrentConnection()
	if err != nil {
		return nil, err
	}
	models := make([]llm.ModelInfo, 0, len(connection.Models))
	for _, catalogEntry := range connection.Models {
		models = append(models, modelInfo(providerRoute, catalogEntry))
	}
	return models, nil
}

func (backend *Adapter) ResolveModel(requestContext context.Context, providerRoute string, modelID string) (llm.ResolvedModelInfo, error) {
	if err := requestContext.Err(); err != nil {
		return llm.ResolvedModelInfo{}, err
	}
	connection, err := backend.connections.CurrentConnection()
	if err != nil {
		return llm.ResolvedModelInfo{}, err
	}
	var configured *CatalogModel
	for index := range connection.Models {
		if connection.Models[index].ID == modelID {
			entry := connection.Models[index]
			configured = &entry
			break
		}
	}
	resolved := llm.ResolvedModelInfo{}
	contextWindow := connection.DefaultContextWindow
	maximumTokens := connection.MaxTokens
	if configured == nil {
		resolved.ModelInfo = llm.ModelInfo{
			Provider:        providerRoute,
			ID:              modelID,
			Name:            modelID,
			InputModalities: []llm.ModelModality{llm.ModalityText},
		}
	} else {
		resolved.ModelInfo = modelInfo(providerRoute, *configured)
		if configured.ContextWindow != nil {
			contextWindow = *configured.ContextWindow
		}
		if configured.MaxTokens != nil {
			maximumTokens = *configured.MaxTokens
		}
	}
	resolved.Context = &llm.ModelContext{
		ContextWindow: contextWindow,
	}
	resolved.DefaultMaxTokens = intPointer(maximumTokens)
	resolved.Reasoning = reasoningInfo(connection.Defaults)
	return resolved, nil
}

func modelInfo(providerRoute string, catalogEntry CatalogModel) llm.ModelInfo {
	modelName := catalogEntry.ID
	if catalogEntry.Name != nil {
		modelName = *catalogEntry.Name
	}
	description := ""
	if catalogEntry.Description != nil {
		description = *catalogEntry.Description
	}
	return llm.ModelInfo{
		Provider:        providerRoute,
		ID:              catalogEntry.ID,
		Name:            modelName,
		Description:     description,
		InputModalities: []llm.ModelModality{llm.ModalityText},
	}
}

func reasoningInfo(defaults RequestDefaults) *llm.ModelReasoningInfo {
	if defaults.Thinking != nil && *defaults.Thinking == ThinkingDisabled {
		return &llm.ModelReasoningInfo{
			Efforts: []llm.ReasoningEffortInfo{
				{
					ID:   llm.ReasoningEffortID(ReasoningOff),
					Name: "Off",
				},
			},
			DefaultEffort: llm.ReasoningEffortID(ReasoningOff),
		}
	}
	defaultEffort := llm.ReasoningEffortID(ReasoningHigh)
	if defaults.ReasoningEffort != nil {
		defaultEffort = llm.ReasoningEffortID(*defaults.ReasoningEffort)
	}
	return &llm.ModelReasoningInfo{
		Efforts: []llm.ReasoningEffortInfo{
			{
				ID:   llm.ReasoningEffortID(ReasoningOff),
				Name: "Off",
			},
			{
				ID:   llm.ReasoningEffortID(ReasoningHigh),
				Name: "High",
			},
			{
				ID:   llm.ReasoningEffortID(ReasoningMax),
				Name: "Max",
			},
		},
		DefaultEffort: defaultEffort,
	}
}
