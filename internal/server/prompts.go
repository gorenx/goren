package server

import (
	"context"
	"errors"
	"time"

	"github.com/gorenx/goren/systemprompt"
	promptactor "github.com/gorenx/goren/systemprompt/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// initializePrompts installs Prompt in the process-owned ActorSystem.
// The caller starts and stops the framework and admits consumers only on success.
func initializePrompts(
	ctx context.Context, system goakt.ActorSystem,
	settings systemprompt.Config,
) (*promptactor.Client, error) {
	if ctx == nil {
		return nil, errors.New("server: prompt context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	endpoint, err := promptactor.NewClient(system, promptactor.Name)
	if err != nil {
		return nil, err
	}
	validated, err := systemprompt.ValidateConfig(settings)
	if err != nil {
		return nil, err
	}
	business, err := systemprompt.NewLayer(validated)
	if err != nil {
		return nil, err
	}
	identity, err := goakt.GrainOf[*promptactor.Grain](ctx, system, promptactor.Name, goakt.WithLongLivedGrain())
	if err != nil {
		return nil, err
	}
	payload, err := system.AskGrain(ctx, identity, promptactor.Initialize{
		Layer: business,
	}, time.Duration(1<<63-1))
	if err != nil {
		return nil, err
	}
	response, ok := payload.(promptactor.Completion)
	if !ok {
		return nil, errors.New("server: invalid prompt initialization response")
	}
	if response.Err != nil {
		return nil, response.Err
	}
	return endpoint, nil
}
