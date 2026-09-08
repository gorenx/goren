package commandsapi

import (
	"context"
	"errors"
	"github.com/gorenx/goren/apiproxy/rpc"
	"github.com/gorenx/goren/commands"
	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/session"
)

// Executor is the command capability bound to one admitted Agent creation.
// It does not expose configuration registration or lifecycle mutation.
type Executor interface {
	List(context.Context) ([]commands.Descriptor, error)
	Execute(context.Context, string, commands.ExecuteOptions) (*commands.Execution, error)
}

// Resolver applies the ordinary Agent access fence and acquires its current
// command capability. A refusal must not return an executable capability.
type Resolver interface {
	ResolveCommands(context.Context, session.SessionID) (Executor, *connection.RPCError)
}

// Gateway maps Remote requests to a current Agent's command capability.
type Gateway struct {
	agents Resolver
}

// NewGateway binds the owner of ordinary Agent access and activation.
func NewGateway(source Resolver) (*Gateway, error) {
	if source == nil {
		return nil, errors.New("apiproxy: Commands resolver is required")
	}
	return &Gateway{
		agents: source,
	}, nil
}

// List queries the effective command directory after Agent access checks.
func (owner *Gateway) List(ctx context.Context, call rpc.Request[ListRequest]) (rpc.Outcome[[]commands.Descriptor], error) {
	endpoint, refused := owner.agents.ResolveCommands(ctx, call.Payload.AgentID)
	if refused != nil {
		return rpc.Fail[[]commands.Descriptor](*refused), nil
	}
	if endpoint == nil {
		return rpc.Outcome[[]commands.Descriptor]{}, errors.New("apiproxy: Commands capability is missing")
	}
	entries, err := endpoint.List(ctx)
	if err != nil {
		return rpc.Outcome[[]commands.Descriptor]{}, err
	}
	return rpc.OK(entries), nil
}

// Execute delegates command execution without scheduling a model turn.
func (owner *Gateway) Execute(ctx context.Context, call rpc.Request[ExecuteRequest]) (rpc.Outcome[*commands.Execution], error) {
	endpoint, refused := owner.agents.ResolveCommands(ctx, call.Payload.AgentID)
	if refused != nil {
		return rpc.Fail[*commands.Execution](*refused), nil
	}
	if endpoint == nil {
		return rpc.Outcome[*commands.Execution]{}, errors.New("apiproxy: Commands capability is missing")
	}
	result, err := endpoint.Execute(ctx, call.Payload.Line, commands.ExecuteOptions{
		AttachmentCount: len(call.Payload.Images),
	})
	if err != nil {
		return rpc.Outcome[*commands.Execution]{}, err
	}
	if result == nil {
		return rpc.Absent[*commands.Execution](), nil
	}
	return rpc.OK(result), nil
}
