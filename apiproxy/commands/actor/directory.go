// Package commandsadapter connects the Commands API to admitted command owners.
package commandsadapter

import (
	"context"
	"errors"
	"fmt"
	"time"

	agentregistry "github.com/gorenx/goren/agent/registry"
	commandsapi "github.com/gorenx/goren/apiproxy/commands"
	"github.com/gorenx/goren/apiproxy/rpc"
	commandactor "github.com/gorenx/goren/commands/actor"
	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/session"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Directory binds command clients after ordinary Agent admission. It owns no
// actors and retains no per-Agent routes or lifecycle state.
type Directory struct {
	system   goakt.ActorSystem
	registry *goakt.GrainIdentity
	timeout  time.Duration
}

// NewDirectory binds the existing lifecycle Registry without activating it.
func NewDirectory(system goakt.ActorSystem, registry *goakt.GrainIdentity, timeout time.Duration) (*Directory, error) {
	if system == nil ||
		registry == nil ||
		timeout <= 0 {
		return nil, errors.New("commands API: Registry and positive timeout required")
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

// ResolveCommands acquires the current command owner without activating it.
// The resulting client retains this creation even if the Session is reopened.
func (source *Directory) ResolveCommands(ctx context.Context, identifier session.SessionID) (commandsapi.Executor, *connection.RPCError) {
	payload, err := source.system.AskGrain(ctx, source.registry, agentregistry.FindAgent{
		AgentID:   identifier,
		WaitReady: true,
	}, source.timeout)
	if err != nil {
		return nil, failure(err)
	}
	ready, ok := payload.(agentregistry.Ready)
	if !ok {
		return nil, failure(errors.New("commands API: invalid Agent resolution reply"))
	}
	if ready.Err != nil {
		return nil, failure(ready.Err)
	}
	if ready.Creation.AgentID != identifier ||
		ready.Registration.ID != identifier ||
		ready.Creation.CreationID == "" ||
		ready.Commands == nil {
		return nil, failure(errors.New("commands API: command owner identity is missing or mismatched"))
	}
	if ready.Registration.ParentID != nil {
		problem := rpc.NewRPCError(connection.ErrorAgentBusy, fmt.Sprintf("session %q is owned by subagent routing", identifier), struct {
			Reason string `json:"reason"`
		}{
			Reason: "use subagent delivery for this child session",
		})
		return nil, &problem
	}
	endpoint, err := commandactor.NewClient(source.system, ready.Commands, ready.Creation.CreationID, source.timeout)
	if err != nil {
		return nil, failure(err)
	}
	return endpoint, nil
}

func failure(err error) *connection.RPCError {
	problem := rpc.NewRPCError(connection.ErrorInternal, err.Error(), struct{}{})
	return &problem
}

var _ commandsapi.Resolver = (*Directory)(nil)
