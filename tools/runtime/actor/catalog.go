// Package runtimeactor adapts tool update ports to existing GoAkt owners.
package runtimeactor

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/tools"
	toolregistry "github.com/gorenx/goren/tools/registry"
	catalogactor "github.com/gorenx/goren/tools/registry/actor"
	toolruntime "github.com/gorenx/goren/tools/runtime"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Catalog is the blocking directory adapter for an update's framework task.
// It must never be called directly from an Actor's Receive method.
type Catalog struct {
	system  goakt.ActorSystem
	target  *goakt.GrainIdentity
	timeout time.Duration
}

var _ toolruntime.Catalog = (*Catalog)(nil)

// NewCatalog binds an existing Registry Grain without creating resources.
func NewCatalog(system goakt.ActorSystem, target *goakt.GrainIdentity, timeout time.Duration) (*Catalog, error) {
	if system == nil || target == nil || timeout <= 0 {
		return nil, errors.New("tools: catalog system, identity and positive timeout are required")
	}
	return &Catalog{
		system:  system,
		target:  target,
		timeout: timeout,
	}, nil
}

// ReadConfiguration reads one registered update; it never substitutes the latest.
func (endpoint *Catalog) ReadConfiguration(ctx context.Context, identifier toolregistry.UpdateID) (toolregistry.Configuration, error) {
	payload, err := endpoint.request(ctx, catalogactor.ReadConfiguration{
		ID: identifier,
	})
	if err != nil {
		return toolregistry.Configuration{}, err
	}
	reply, ok := payload.(catalogactor.Configuration)
	if !ok {
		return toolregistry.Configuration{}, fmt.Errorf("tools: invalid configuration reply %T", payload)
	}
	if reply.Err != nil {
		return toolregistry.Configuration{}, reply.Err
	}
	if !reply.Found {
		return toolregistry.Configuration{}, errors.New("tools: configuration update is unavailable")
	}
	return reply.Value, nil
}

// Publish preserves both committed definitions and a possible notification error.
func (endpoint *Catalog) Publish(ctx context.Context, identifier toolregistry.UpdateID) (tools.Definitions, error) {
	payload, err := endpoint.request(ctx, catalogactor.PublishDefinitions{
		ID: identifier,
	})
	if err != nil {
		return tools.Definitions{}, err
	}
	reply, ok := payload.(catalogactor.Catalog)
	if !ok {
		return tools.Definitions{}, fmt.Errorf("tools: invalid catalog publication reply %T", payload)
	}
	if reply.Err == nil && !reply.Found {
		return tools.Definitions{}, errors.New("tools: publication reply contains no committed catalog")
	}
	return reply.Definitions, reply.Err
}

// Lookup distinguishes an unknown update from a retained unpublished transaction.
func (endpoint *Catalog) Lookup(ctx context.Context, identifier toolregistry.UpdateID) (toolregistry.Publication, bool, error) {
	payload, err := endpoint.request(ctx, catalogactor.ReadPublication{
		ID: identifier,
	})
	if err != nil {
		return toolregistry.Publication{}, false, err
	}
	reply, ok := payload.(catalogactor.Outcome)
	if !ok {
		return toolregistry.Publication{}, false, fmt.Errorf("tools: invalid publication lookup reply %T", payload)
	}
	return reply.Publication, reply.Found, nil
}

// Abort forgets a batch only after the update has cleaned unpublished candidates.
func (endpoint *Catalog) Abort(ctx context.Context, identifier toolregistry.UpdateID) error {
	return endpoint.complete(ctx, catalogactor.AbortUpdate{
		ID: identifier,
	})
}

// Settle forgets the update after all published implementations and resources settle.
func (endpoint *Catalog) Settle(ctx context.Context, identifier toolregistry.UpdateID) error {
	return endpoint.complete(ctx, catalogactor.SettleUpdate{
		ID: identifier,
	})
}

func (endpoint *Catalog) complete(ctx context.Context, message any) error {
	payload, err := endpoint.request(ctx, message)
	if err != nil {
		return err
	}
	reply, ok := payload.(catalogactor.Completion)
	if !ok {
		return fmt.Errorf("tools: invalid catalog completion reply %T", payload)
	}
	return reply.Err
}

// request is the private framework erasure boundary; exported methods stay typed.
func (endpoint *Catalog) request(ctx context.Context, message any) (any, error) {
	if ctx == nil {
		return nil, errors.New("tools: catalog request context is required")
	}
	if ctx.Err() != nil {
		return nil, context.Cause(ctx)
	}
	return endpoint.system.AskGrain(ctx, endpoint.target, message, endpoint.timeout)
}

// Configure registers complete overrides under an exact update identity. A retained
// configuration and a notification error may coexist; callers must preserve both.
func (endpoint *Catalog) Configure(ctx context.Context, identifier toolregistry.UpdateID, agentID session.SessionID, overrides []toolregistry.Tool) (toolregistry.Configuration, error) {
	payload, err := endpoint.request(ctx, catalogactor.RegisterConfiguration{
		ID:        identifier,
		AgentID:   agentID,
		Overrides: overrides,
	})
	if err != nil {
		return toolregistry.Configuration{}, err
	}
	reply, valid := payload.(catalogactor.Configuration)
	if !valid {
		return toolregistry.Configuration{}, fmt.Errorf("tools: invalid configuration registration reply %T", payload)
	}
	if !reply.Found {
		if reply.Err != nil {
			return toolregistry.Configuration{}, reply.Err
		}
		return toolregistry.Configuration{}, errors.New("tools: configuration registration returned no retained configuration")
	}
	if reply.Value.Update.ID != identifier || reply.Value.Update.AgentID != agentID {
		return toolregistry.Configuration{}, errors.New("tools: registered configuration does not match the request")
	}
	return reply.Value, reply.Err
}
