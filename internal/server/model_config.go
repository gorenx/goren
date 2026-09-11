package server

import (
	"context"
	"errors"
	"time"

	"github.com/gorenx/goren/llm"
	configactor "github.com/gorenx/goren/llm/actor"
	"github.com/gorenx/goren/llm/deepseek"
	"github.com/gorenx/goren/llm/modelconfig"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

// initializeModels installs ModelConfig in the process-owned ActorSystem.
// The caller starts and stops the framework and admits consumers only on success.
func initializeModels(
	ctx context.Context, system goakt.ActorSystem,
	settings modelconfig.Definition,
) (*configactor.ModelsClient, error) {
	if ctx == nil {
		return nil, errors.New("server: model configuration context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	endpoint, err := configactor.NewModelsClient(system)
	if err != nil {
		return nil, err
	}
	business, err := modelconfig.NewCatalog(settings)
	if err != nil {
		return nil, err
	}
	identity, err := goakt.GrainOf[*configactor.Grain](ctx, system, configactor.Name, goakt.WithLongLivedGrain(), goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		return nil, err
	}
	payload, err := system.AskGrain(ctx, identity, configactor.InitializeModels{
		Catalog: business,
	}, time.Duration(1<<63-1))
	if err != nil {
		return nil, err
	}
	response, ok := payload.(configactor.Completion)
	if !ok {
		return nil, errors.New("server: invalid model configuration initialization response")
	}
	if response.Err != nil {
		return nil, response.Err
	}
	return endpoint, nil
}

// DefaultModelConfig preserves the default deployment's route and credential reference.
func DefaultModelConfig() modelconfig.Definition {
	return modelconfig.Definition{
		Default: llm.CallConfig{
			Provider: deepseek.ProviderRoute,
			Model:    deepseek.DefaultModelID,
		},
		Providers: []modelconfig.Provider{
			{
				ID:            deepseek.ProviderRoute,
				CredentialRef: deepseek.DefaultAPIKeyEnv,
			},
		},
	}
}
