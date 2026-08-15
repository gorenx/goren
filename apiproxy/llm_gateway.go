package apiproxy

import (
	"context"
	"errors"

	"github.com/gorenx/goren/llm"
)

// LLMDirectory is the consumer-owned model topology required by the Host API.
type LLMDirectory interface {
	ListProviders() []llm.ProviderInfo
	ListConfigurableProviders() []llm.ConfigurableProvider
	ListModels(context.Context, string) ([]llm.ModelInfo, error)
	ResolveModelInfo(context.Context, string, string) (llm.ResolvedModelInfo, error)
}

// LLMGateway projects the live LLM runtime into Host-scoped wire views.
type LLMGateway struct {
	directory LLMDirectory
}

// NewLLMGateway creates the Host-scoped provider and model catalog gateway.
func NewLLMGateway(directory LLMDirectory) (*LLMGateway, error) {
	if directory == nil {
		return nil, errors.New("apiproxy: LLM Gateway directory is nil")
	}
	return &LLMGateway{directory: directory}, nil
}

// Providers merges configurable declarations with active adapter routes.
func (owner *LLMGateway) Providers(
	_ context.Context,
	_ Request[LLMProvidersRequest],
) (Outcome[LLMProvidersValue], error) {
	registered := owner.directory.ListProviders()
	active := make(map[string]struct{}, len(registered))
	for _, route := range registered {
		active[route.ID] = struct{}{}
	}

	declarations := owner.directory.ListConfigurableProviders()
	declared := make(map[string]struct{}, len(declarations))
	providerViews := make([]ConfigurableProviderView, 0, len(declarations)+len(registered))
	for _, declaration := range declarations {
		_, isActive := active[declaration.Provider]
		declared[declaration.Provider] = struct{}{}
		providerViews = append(providerViews, ConfigurableProviderView{
			Provider: declaration.Provider, DisplayName: declaration.DisplayName,
			SettingsNS: declaration.SettingsNS, SettingsPath: append([]string{}, declaration.SettingsPath...),
			Active: isActive, Declared: cloneBoolPointer(declaration.Declared),
		})
	}
	for _, route := range registered {
		if _, exists := declared[route.ID]; exists {
			continue
		}
		providerViews = append(providerViews, ConfigurableProviderView{
			Provider: route.ID, DisplayName: route.Name, SettingsNS: "", SettingsPath: []string{}, Active: true,
		})
	}
	return OK(LLMProvidersValue{Providers: providerViews}), nil
}

// Models returns the Host-scoped model catalog without a Session selection.
func (owner *LLMGateway) Models(
	requestContext context.Context,
	_ Request[LLMModelsRequest],
) (Outcome[LLMModelsValue], error) {
	groups, failures := owner.Catalog(requestContext)
	return OK(LLMModelsValue{Groups: groups, Failures: failures}), nil
}

// Catalog resolves every active provider independently so one broken route
// does not hide sound provider groups.
func (owner *LLMGateway) Catalog(requestContext context.Context) ([]ModelProviderGroup, []ModelCatalogFailure) {
	registered := owner.directory.ListProviders()
	groups := make([]ModelProviderGroup, 0, len(registered))
	failures := make([]ModelCatalogFailure, 0)
	for _, route := range registered {
		listedModels, err := owner.directory.ListModels(requestContext, route.ID)
		if err != nil {
			failures = append(failures, ModelCatalogFailure{ID: route.ID, Name: route.Name, Message: err.Error()})
			continue
		}
		modelEntries := make([]ModelCatalogModel, 0, len(listedModels))
		failed := false
		for _, listed := range listedModels {
			resolved, resolveErr := owner.directory.ResolveModelInfo(requestContext, route.ID, listed.ID)
			if resolveErr != nil {
				failures = append(failures, ModelCatalogFailure{
					ID: route.ID, Name: route.Name, Message: resolveErr.Error(),
				})
				failed = true
				break
			}
			model := ModelCatalogModel{ID: listed.ID, Name: listed.Name, Description: listed.Description}
			if resolved.Reasoning != nil {
				reasoning := &ModelReasoning{
					Efforts:       make([]ModelReasoningEffort, 0, len(resolved.Reasoning.Efforts)),
					DefaultEffort: string(resolved.Reasoning.DefaultEffort),
				}
				for _, effort := range resolved.Reasoning.Efforts {
					reasoning.Efforts = append(reasoning.Efforts, ModelReasoningEffort{
						ID: string(effort.ID), Name: effort.Name, Description: effort.Description,
					})
				}
				model.Reasoning = reasoning
			}
			modelEntries = append(modelEntries, model)
		}
		if !failed && len(modelEntries) != 0 {
			groups = append(groups, ModelProviderGroup{ID: route.ID, Name: route.Name, Models: modelEntries})
		}
	}
	return groups, failures
}

func cloneBoolPointer(source *bool) *bool {
	if source == nil {
		return nil
	}
	detached := *source
	return &detached
}
