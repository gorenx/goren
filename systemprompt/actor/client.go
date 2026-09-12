package promptactor

import (
	"context"
	"errors"
	"github.com/gorenx/goren/systemprompt"
	goakt "github.com/tochemey/goakt/v4/actor"
	"slices"
	"time"
)

// Name is the stable server-local prompt registry identity.
const Name = "prompt-registry"

// Client is an immutable non-actor boundary. Actor callers use RequestGrain directly.
type Client struct {
	system goakt.ActorSystem
	name   string
}

// NewClient binds a named prompt owner in the shared ActorSystem.
func NewClient(running goakt.ActorSystem, name string) (*Client, error) {
	if running == nil || name == "" {
		return nil, ErrUnavailable
	}
	return &Client{
		system: running,
		name:   name,
	}, nil
}

func (endpoint *Client) call(ctx context.Context, message any) (any, error) {
	if ctx == nil {
		return nil, errors.New("systemprompt: request context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	identity, err := goakt.GrainOf[*Grain](ctx, endpoint.system, endpoint.name, goakt.WithLongLivedGrain())
	if err != nil {
		return nil, err
	}
	return endpoint.system.AskGrain(ctx, identity, message, time.Duration(1<<63-1))
}

// Prepare obtains a versioned, immutable request plan without evaluating strategies.
func (endpoint *Client) Prepare(ctx context.Context) (Prepared, error) {
	payload, err := endpoint.call(ctx, Prepare{})
	if err != nil {
		return Prepared{}, err
	}
	reply, ok := payload.(Prepared)
	if !ok {
		return Prepared{}, errors.New("systemprompt: invalid preparation response")
	}
	return reply, reply.Err
}

// Assemble evaluates the captured snapshot outside the registry mailbox.
func (endpoint *Client) Assemble(ctx context.Context, input systemprompt.AssembleContext) (systemprompt.PromptAssembly, error) {
	reply, err := endpoint.Prepare(ctx)
	if err != nil {
		return systemprompt.PromptAssembly{}, err
	}
	return reply.Snapshot.Assemble(ctx, input)
}

func (endpoint *Client) register(ctx context.Context, command Register) (systemprompt.RegistrationID, error) {
	payload, err := endpoint.call(ctx, command)
	if err != nil {
		return "", err
	}
	reply, ok := payload.(Registered)
	if !ok {
		return "", errors.New("systemprompt: invalid registration response")
	}
	if reply.Err != nil {
		return "", reply.Err
	}
	return reply.ID, nil
}

// AddSection registers one ordered system instruction.
func (endpoint *Client) AddSection(ctx context.Context, definition systemprompt.PromptSection) (systemprompt.RegistrationID, error) {
	return endpoint.register(ctx, Section{
		Definition: definition,
	})
}

// AddContext registers one runtime-context provider.
func (endpoint *Client) AddContext(ctx context.Context, definition systemprompt.PromptContext) (systemprompt.RegistrationID, error) {
	return endpoint.register(ctx, Context{
		Definition: definition,
	})
}

// AddVariable registers one interpolation provider.
func (endpoint *Client) AddVariable(ctx context.Context, name string, provider systemprompt.VariableProvider) (systemprompt.RegistrationID, error) {
	return endpoint.register(ctx, Variable{
		Name:     name,
		Provider: provider,
	})
}

// AddRuntimeContextSuppressor suppresses context in the effective plan.
func (endpoint *Client) AddRuntimeContextSuppressor(ctx context.Context, name string) (systemprompt.RegistrationID, error) {
	return endpoint.register(ctx, Suppressor{
		Name: name,
	})
}

// Remove deletes exactly the registration identified by the caller.
func (endpoint *Client) Remove(ctx context.Context, identifier systemprompt.RegistrationID) (bool, error) {
	payload, err := endpoint.call(ctx, Remove{
		ID: identifier,
	})
	if err != nil {
		return false, err
	}
	reply, ok := payload.(Completion)
	if !ok {
		return false, errors.New("systemprompt: invalid removal response")
	}
	return reply.Removed, reply.Err
}

var _ systemprompt.SystemPrompt = (*Client)(nil)

// ApplyBaseline updates the shared configuration consumed at subsequent Turn boundaries.
// It does not mutate an Agent's already captured prompt or own its lifecycle.
func (endpoint *Client) ApplyBaseline(ctx context.Context, input systemprompt.Config) error {
	detached := systemprompt.Config{
		Persona:   input.Persona,
		ToolOrder: slices.Clone(input.ToolOrder),
	}
	if input.IncludeHarnessIdentity != nil {
		selected := *input.IncludeHarnessIdentity
		detached.IncludeHarnessIdentity = &selected
	}
	if input.IncludeRuntimeContext != nil {
		selected := *input.IncludeRuntimeContext
		detached.IncludeRuntimeContext = &selected
	}
	payload, err := endpoint.call(ctx, ApplyBaseline{
		Context: ctx,
		Config:  detached,
	})
	if err != nil {
		return err
	}
	reply, ok := payload.(Completion)
	if !ok {
		return errors.New("systemprompt: invalid baseline response")
	}
	return reply.Err
}
