package server

import (
	"context"
	"errors"
	sessionapi "github.com/gorenx/goren/apiproxy/session"
	"github.com/gorenx/goren/session/projection"
	"time"

	projectionactor "github.com/gorenx/goren/session/projection/actor"
	"github.com/gorenx/goren/session/title"
	titleregistry "github.com/gorenx/goren/session/title/registry"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

// initializeTitles configures the shared lifecycle owner before Agent admission.
// Agent Registry starts individual Title components through the returned route.
func initializeTitles(
	ctx context.Context,
	system goakt.ActorSystem,
	settings title.Config,
	provider title.Provider,
	timeout time.Duration,
) (*goakt.GrainIdentity, error) {
	if ctx == nil || system == nil || timeout <= 0 {
		return nil, errors.New("server: Title context, ActorSystem and positive timeout are required")
	}
	err := ctx.Err()
	if err != nil {
		return nil, err
	}
	validated, err := settings.Validate()
	if err != nil {
		return nil, err
	}
	if validated.LLM != nil && provider == nil {
		return nil, errors.New("server: configured model-backed titles require a Provider")
	}
	endpoint, err := goakt.GrainOf[*titleregistry.Grain](ctx, system, titleregistry.Name,
		goakt.WithLongLivedGrain(),
		goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		return nil, err
	}
	payload, err := system.AskGrain(ctx, endpoint, titleregistry.Configure{
		Config:   validated,
		Provider: provider,
		Timeout:  timeout,
	}, timeout)
	if err != nil {
		return endpoint, err
	}
	result, valid := payload.(titleregistry.Configured)
	if !valid {
		return endpoint, errors.New("server: invalid Title configuration reply")
	}
	return endpoint, result.Err
}

// initializeProjections installs the default read computations before API admission.
// The process owns the shared Grain; individual reads do not create projections elsewhere.
func initializeProjections(ctx context.Context, system goakt.ActorSystem, timeout time.Duration) (*goakt.GrainIdentity, error) {
	if ctx == nil || system == nil || timeout <= 0 {
		return nil, errors.New("server: projection context, ActorSystem and positive timeout are required")
	}
	endpoint, err := goakt.GrainOf[*projectionactor.Grain](ctx, system, "projections", goakt.WithLongLivedGrain())
	if err != nil {
		return nil, err
	}
	for _, unit := range []projection.Unit{title.Unit{}, sessionapi.SessionListMetadataUnit()} {
		payload, failure := system.AskGrain(ctx, endpoint, projectionactor.Register{
			Unit: unit,
		}, timeout)
		if failure != nil {
			return endpoint, failure
		}
		result, valid := payload.(projectionactor.Registered)
		if !valid {
			return endpoint, errors.New("server: invalid projection registration reply")
		}
		if result.Err != nil {
			return endpoint, result.Err
		}
	}
	return endpoint, nil
}
