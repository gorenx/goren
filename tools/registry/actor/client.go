package toolactor

import (
	"context"
	"errors"
	"time"

	"github.com/gorenx/goren/session"
	toolregistry "github.com/gorenx/goren/tools/registry"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Client accesses configuration owned by the Tools Registry. It owns no catalog
// data, Actor lifetime or tool implementations.
type Client struct {
	system  goakt.ActorSystem
	target  *goakt.GrainIdentity
	timeout time.Duration
}

// NewClient binds an existing Schema Registry without starting Actors.
func NewClient(system goakt.ActorSystem, target *goakt.GrainIdentity, timeout time.Duration) (*Client, error) {
	if system == nil || target == nil || timeout <= 0 {
		return nil, errors.New("tools: Registry endpoint and timeout are required")
	}
	return &Client{
		system:  system,
		target:  target,
		timeout: timeout,
	}, nil
}

// Read returns the Session Registry configuration before MCP discovery.
func (endpoint *Client) Read(ctx context.Context, identifier session.SessionID) (toolregistry.Configuration, error) {
	payload, err := endpoint.system.AskGrain(
		ctx,
		endpoint.target,
		ReadConfiguration{
			SessionID: identifier,
		},
		endpoint.timeout,
	)
	if err != nil {
		return toolregistry.Configuration{}, err
	}
	reply, valid := payload.(Configuration)
	if !valid {
		return toolregistry.Configuration{}, errors.New("tools: invalid Registry configuration reply")
	}
	return reply.Value, nil
}

// ConfigureSession records an explicitly supplied Session catalog before initialization.
func (endpoint *Client) ConfigureSession(ctx context.Context, identifier session.SessionID, input toolregistry.Catalog) error {
	payload, err := endpoint.system.AskGrain(
		ctx,
		endpoint.target,
		ConfigureSession{
			SessionID: identifier,
			Catalog:   input,
		},
		endpoint.timeout,
	)
	if err != nil {
		return err
	}
	reply, valid := payload.(Completion)
	if !valid {
		return errors.New("tools: invalid Session configuration reply")
	}
	return reply.Err
}
