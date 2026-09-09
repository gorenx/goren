package server

import (
	"context"
	"errors"
	"time"

	"github.com/gorenx/goren/credentials"
	"github.com/gorenx/goren/llm"
	llmactor "github.com/gorenx/goren/llm/actor"
	"github.com/gorenx/goren/llm/deepseek"
	"github.com/gorenx/goren/llm/deepseek/anonymoususerid"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

// initializeDeepSeek installs the concrete provider without resolving a secret
// or starting network I/O. The process owns the returned registration, including
// a nonempty ID accompanied by a publication error.
func initializeDeepSeek(ctx context.Context, system goakt.ActorSystem, settings deepseek.Config, environment anonymoususerid.Environment, secrets credentials.Provider) (llm.RegistrationID, error) {
	if ctx == nil || system == nil {
		return "", errors.New("server: provider initialization requires context and actor system")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	connection, err := deepseek.NewConnection(settings, environment, secrets)
	if err != nil {
		return "", err
	}
	identity, err := deepseek.NewIdentity(environment)
	if err != nil {
		return "", err
	}
	provider, err := deepseek.NewAdapter(deepseek.AdapterDependencies{
		Connections: connection,
		Credentials: connection,
		Identity:    identity,
	})
	if err != nil {
		return "", err
	}
	target, err := goakt.GrainOf[*llmactor.Grain](ctx, system, llmactor.Name, goakt.WithLongLivedGrain(), goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		return "", err
	}
	payload, err := system.AskGrain(ctx, target, llmactor.RegisterAdapter{
		Providers: []string{deepseek.ProviderRoute},
		Adapter:   provider,
	}, time.Duration(1<<63-1))
	if err != nil {
		return "", err
	}
	registered, ok := payload.(llmactor.Registered)
	if !ok {
		return "", errors.New("server: invalid provider registration response")
	}
	return registered.ID, registered.Err
}
