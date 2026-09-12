// Package mcpadapter connects Session MCP management to the admitted Tools owner.
package mcpadapter

import (
	"context"
	"errors"
	"time"

	agentregistry "github.com/gorenx/goren/agent/registry"
	mcpapi "github.com/gorenx/goren/apiproxy/mcp"
	"github.com/gorenx/goren/session"
	runtimeactor "github.com/gorenx/goren/tools/runtime/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Directory resolves current Agent lifetimes; it owns no connection or route map.
type Directory struct {
	system   goakt.ActorSystem
	registry *goakt.GrainIdentity
	timeout  time.Duration
}

// NewDirectory binds the existing Agent Registry without creating a Session.
func NewDirectory(system goakt.ActorSystem, registry *goakt.GrainIdentity, timeout time.Duration) (*Directory, error) {
	if system == nil || registry == nil || timeout <= 0 {
		return nil, errors.New("MCP API: Registry and positive timeout are required")
	}
	if err := registry.Validate(); err != nil {
		return nil, err
	}
	return &Directory{
		system:   system,
		registry: registry,
		timeout:  timeout,
	}, nil
}

// ListConnections queries transport state after ordinary Agent admission.
func (source *Directory) ListConnections(ctx context.Context, identifier session.SessionID) ([]mcpapi.View, error) {
	creation, address, err := source.resolve(ctx, identifier)
	if err != nil {
		return nil, err
	}
	payload, err := source.system.AskGrain(ctx, address, runtimeactor.ReadConnections{
		CreationID: creation.CreationID,
	}, source.timeout)
	if err != nil {
		return nil, err
	}
	result, valid := payload.(runtimeactor.Connections)
	if !valid {
		return nil, errors.New("MCP API: invalid connection state reply")
	}
	if result.Err != nil {
		return nil, result.Err
	}
	entries := make([]mcpapi.View, 0, len(result.Values))
	for _, entry := range result.Values {
		entries = append(entries, mcpapi.View{
			ServerName: entry.Name,
			Transport:  entry.Transport,
			Status:     string(entry.Status),
			ToolCount:  entry.ToolCount,
		})
	}
	return entries, nil
}

// RetryConnection restores only the selected Session's configured target.
// The operation's own Context is passed through to connection recovery.
func (source *Directory) RetryConnection(ctx context.Context, input mcpapi.RetryRequest) error {
	bounded, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	creation, address, err := source.resolve(bounded, input.SessionID)
	if err != nil {
		return err
	}
	payload, err := source.system.AskGrain(bounded, address, runtimeactor.RetryConnection{
		CreationID: creation.CreationID,
		Name:       input.ServerName,
		Context:    bounded,
	}, 35*time.Second)
	if err != nil {
		return err
	}
	result, valid := payload.(runtimeactor.Completion)
	if !valid {
		return errors.New("MCP API: invalid recovery reply")
	}
	return result.Err
}

func (source *Directory) resolve(ctx context.Context, identifier session.SessionID) (agentregistry.Creation, *goakt.GrainIdentity, error) {
	payload, err := source.system.AskGrain(ctx, source.registry, agentregistry.FindAgent{
		AgentID: identifier,
	}, source.timeout)
	if err != nil {
		return agentregistry.Creation{}, nil, err
	}
	ready, valid := payload.(agentregistry.Ready)
	if !valid {
		return agentregistry.Creation{}, nil, errors.New("MCP API: invalid Agent resolution reply")
	}
	if ready.Err != nil {
		return agentregistry.Creation{}, nil, ready.Err
	}
	if identifier == "" || ready.Creation.AgentID != identifier || ready.Registration.ID != identifier || ready.Creation.CreationID == "" {
		return agentregistry.Creation{}, nil, errors.New("MCP API: mismatched Agent creation")
	}
	address, err := goakt.GrainOf[*runtimeactor.Runtime](ctx, source.system, runtimeactor.RuntimeName(string(identifier)))
	return ready.Creation, address, err
}

var _ mcpapi.Directory = (*Directory)(nil)
