package server

import (
	"context"
	"errors"
	"time"

	"github.com/gorenx/goren/credentials"
	credentialactor "github.com/gorenx/goren/credentials/actor"
	credentialslocal "github.com/gorenx/goren/credentials/local"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// initializeCredentials installs Credentials in the process-owned ActorSystem.
// The caller starts and stops the framework and admits consumers only on success.
func initializeCredentials(
	ctx context.Context, system goakt.ActorSystem,
	settings credentialslocal.Config, platform credentials.Environment,
) (*credentialactor.Client, error) {
	if ctx == nil {
		return nil, errors.New("server: credentials context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	endpoint, err := credentialactor.NewClient(system)
	if err != nil {
		return nil, err
	}
	storage, err := credentialslocal.NewLiveStore(settings)
	if err != nil {
		return nil, err
	}
	business, err := credentials.NewManager(storage, platform)
	if err != nil {
		return nil, err
	}
	identity, err := goakt.GrainOf[*credentialactor.Grain](ctx, system, credentialactor.Name, goakt.WithLongLivedGrain())
	if err != nil {
		return nil, err
	}
	payload, err := system.AskGrain(ctx, identity, credentialactor.Initialize{
		Manager: business,
	}, time.Duration(1<<63-1))
	if err != nil {
		return nil, err
	}
	response, ok := payload.(credentialactor.Completion)
	if !ok {
		return nil, errors.New("server: invalid credentials initialization response")
	}
	if response.Err != nil {
		return nil, response.Err
	}
	return endpoint, nil
}
