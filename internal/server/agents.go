package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	agentactor "github.com/gorenx/goren/agent/actor"
	agentregistry "github.com/gorenx/goren/agent/registry"
	toolregistry "github.com/gorenx/goren/tools/registry"
	toolactor "github.com/gorenx/goren/tools/registry/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// closeAgents freezes admission before obtaining root creations. Registry owns
// descendant cleanup and Session flush. Failure retains framework and storage
// responsibility with the caller; it must not stop them until a retry succeeds.
func closeAgents(ctx context.Context, system goakt.ActorSystem, registry *goakt.GrainIdentity) error {
	payload, err := system.AskGrain(ctx, registry, agentregistry.StopAdmission{}, time.Duration(1<<63-1))
	if err != nil {
		return err
	}
	roots, ok := payload.(agentregistry.Roots)
	if !ok {
		return errors.New("server: invalid Agent shutdown inventory")
	}
	var failures []error
	for _, creation := range roots.Creations {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		payload, err := system.AskGrain(ctx, registry, agentregistry.CloseAgent{
			Creation: creation,
		}, time.Duration(1<<63-1))
		if err == nil {
			closed, ok := payload.(agentactor.Closed)
			if !ok {
				err = errors.New("invalid Agent close response")
			} else {
				err = closed.Err
			}
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("close Agent %q: %w", creation.AgentID, err))
		}
	}
	return errors.Join(failures...)
}

// initializeTools registers explicit process defaults before Agent admission.
// Providers remain unopened until the per-Agent lifecycle applies configuration.
func initializeTools(ctx context.Context, system goakt.ActorSystem, entries []toolregistry.Tool) (*goakt.GrainIdentity, error) {
	if ctx == nil || system == nil {
		return nil, errors.New("server: tool initialization context and ActorSystem are required")
	}
	err := ctx.Err()
	if err != nil {
		return nil, err
	}
	identity, err := goakt.GrainOf[*toolactor.RegistryGrain](ctx, system, toolactor.RegistryName, goakt.WithLongLivedGrain())
	if err != nil {
		return nil, err
	}
	payload, err := system.AskGrain(ctx, identity, toolactor.InitializeDefaults{
		Tools: entries,
	}, time.Duration(1<<63-1))
	if err != nil {
		return nil, err
	}
	initialized, valid := payload.(toolactor.Completion)
	if !valid {
		return nil, errors.New("server: invalid tool initialization response")
	}
	if initialized.Err != nil {
		return nil, initialized.Err
	}
	return identity, nil
}
