package runtimeactor

import (
	"context"
	"errors"
	"time"

	"github.com/gorenx/goren/session"
	toolregistry "github.com/gorenx/goren/tools/registry"
	catalogactor "github.com/gorenx/goren/tools/registry/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Catalog reads unmerged Registry configuration from a framework task.
type Catalog struct {
	system  goakt.ActorSystem
	target  *goakt.GrainIdentity
	timeout time.Duration
}

// NewCatalog binds an existing Schema Registry without starting Actors.
func NewCatalog(system goakt.ActorSystem, target *goakt.GrainIdentity, timeout time.Duration) (*Catalog, error) {
	if system == nil || target == nil || timeout <= 0 {
		return nil, errors.New("tools: Registry endpoint and timeout are required")
	}
	return &Catalog{
		system:  system,
		target:  target,
		timeout: timeout,
	}, nil
}

// Read returns global and custom facts; it does not interpret policy or merge schemas.
func (endpoint *Catalog) Read(ctx context.Context, identifier session.SessionID) (toolregistry.Configuration, error) {
	payload, err := endpoint.system.AskGrain(ctx, endpoint.target, catalogactor.ReadConfiguration{SessionID: identifier}, endpoint.timeout)
	if err != nil {
		return toolregistry.Configuration{}, err
	}
	reply, valid := payload.(catalogactor.Configuration)
	if !valid {
		return toolregistry.Configuration{}, errors.New("tools: invalid Registry configuration reply")
	}
	return reply.Value, nil
}

// ConfigureSession records explicitly supplied custom values before initialization.
func (endpoint *Catalog) ConfigureSession(ctx context.Context, identifier session.SessionID, input toolregistry.Catalog) error {
	payload, err := endpoint.system.AskGrain(ctx, endpoint.target, catalogactor.ConfigureSession{
		SessionID: identifier,
		Catalog:   input,
	}, endpoint.timeout)
	if err != nil {
		return err
	}
	reply, valid := payload.(catalogactor.Completion)
	if !valid {
		return errors.New("tools: invalid Session configuration reply")
	}
	return reply.Err
}
