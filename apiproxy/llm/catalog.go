package llmapi

import (
	"context"
	"errors"

	"github.com/gorenx/goren/llm"
)

// Directory is the consumer-owned model topology required by the Host API.
type Directory interface {
	ReadCatalog(context.Context) (ProviderCatalog, error)
	ListModels(context.Context, string) ([]llm.ModelInfo, error)
	ResolveModelInfo(context.Context, string, string) (llm.ResolvedModelInfo, error)
}

// ProviderCatalog captures active routes and configurable declarations from one read.
type ProviderCatalog struct {
	Active       []llm.ProviderInfo
	Configurable []llm.ConfigurableProvider
}

// Catalog owns provider and model directory projection independently of RPC.
type Catalog struct {
	directory Directory
}

// NewCatalog binds the model directory query capability.
func NewCatalog(source Directory) (*Catalog, error) {
	if source == nil {
		return nil, errors.New("apiproxy: LLM Gateway directory is nil")
	}
	return &Catalog{
		directory: source,
	}, nil
}

// Providers merges configurable declarations with active adapter routes.
func (owner *Catalog) Providers(
	requestContext context.Context,
) (ProvidersValue, error) {
	snapshot, err := owner.directory.ReadCatalog(requestContext)
	if err != nil {
		return ProvidersValue{}, err
	}
	registered := snapshot.Active
	active := make(map[string]struct{}, len(registered))
	for _, route := range registered {
		active[route.ID] = struct{}{}
	}

	declarations := snapshot.Configurable
	declared := make(map[string]struct{}, len(declarations))
	providerViews := make([]ConfigurableProviderView, 0, len(declarations)+len(registered))
	for _, declaration := range declarations {
		_, isActive := active[declaration.Provider]
		declared[declaration.Provider] = struct{}{}
		providerViews = append(providerViews, ConfigurableProviderView{
			Provider:     declaration.Provider,
			DisplayName:  declaration.DisplayName,
			SettingsNS:   declaration.SettingsNS,
			SettingsPath: append([]string{}, declaration.SettingsPath...),
			Active:       isActive,
			Declared:     cloneBoolPointer(declaration.Declared),
		})
	}
	for _, route := range registered {
		if _, exists := declared[route.ID]; exists {
			continue
		}
		providerViews = append(providerViews, ConfigurableProviderView{
			Provider:     route.ID,
			DisplayName:  route.Name,
			SettingsNS:   "",
			SettingsPath: []string{},
			Active:       true,
		})
	}
	return ProvidersValue{
		Providers: providerViews,
	}, nil
}

// List resolves every active provider independently so one broken route
// does not hide sound provider groups.
func (owner *Catalog) List(requestContext context.Context) ([]ModelProviderGroup, []ModelCatalogFailure, error) {
	snapshot, err := owner.directory.ReadCatalog(requestContext)
	if err != nil {
		return nil, nil, err
	}
	registered := snapshot.Active
	groups := make([]ModelProviderGroup, 0, len(registered))
	failures := make([]ModelCatalogFailure, 0)
	for _, route := range registered {
		listedModels, err := owner.directory.ListModels(requestContext, route.ID)
		if err != nil {
			failures = append(failures, ModelCatalogFailure{
				ID:      route.ID,
				Name:    route.Name,
				Message: err.Error(),
			})
			continue
		}
		modelEntries := make([]ModelCatalogModel, 0, len(listedModels))
		failed := false
		for _, listed := range listedModels {
			resolved, resolveErr := owner.directory.ResolveModelInfo(requestContext, route.ID, listed.ID)
			if resolveErr != nil {
				failures = append(failures, ModelCatalogFailure{
					ID:      route.ID,
					Name:    route.Name,
					Message: resolveErr.Error(),
				})
				failed = true
				break
			}
			model := ModelCatalogModel{
				ID:          listed.ID,
				Name:        listed.Name,
				Description: listed.Description,
			}
			if resolved.Reasoning != nil {
				reasoning := &ModelReasoning{
					Efforts:       make([]ModelReasoningEffort, 0, len(resolved.Reasoning.Efforts)),
					DefaultEffort: string(resolved.Reasoning.DefaultEffort),
				}
				for _, effort := range resolved.Reasoning.Efforts {
					reasoning.Efforts = append(reasoning.Efforts, ModelReasoningEffort{
						ID:          string(effort.ID),
						Name:        effort.Name,
						Description: effort.Description,
					})
				}
				model.Reasoning = reasoning
			}
			modelEntries = append(modelEntries, model)
		}
		if !failed && len(modelEntries) != 0 {
			groups = append(groups, ModelProviderGroup{
				ID:     route.ID,
				Name:   route.Name,
				Models: modelEntries,
			})
		}
	}
	return groups, failures, nil
}

func cloneBoolPointer(source *bool) *bool {
	if source == nil {
		return nil
	}
	detached := *source
	return &detached
}
