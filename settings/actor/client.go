package settingsactor

import (
	"context"
	"time"

	"github.com/gorenx/goren/settings"
	"github.com/gorenx/goren/subagent/companion"
	"github.com/gorenx/goren/tools"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

// Client exposes typed Settings operations to non-mailbox consumers. Actor
// consumers send these messages using their own RequestGrain mechanism.
type Client struct {
	system   goakt.ActorSystem
	identity *goakt.GrainIdentity
}

// NewClient resolves the single Settings Grain in a borrowed ActorSystem.
// It acquires no store or business service; requests remain unavailable until
// Provider.Bind initializes that owner and composition admits network traffic.
func NewClient(ctx context.Context, running goakt.ActorSystem) (*Client, error) {
	if ctx == nil || running == nil {
		return nil, ErrUnavailable
	}
	identity, err := goakt.GrainOf[*Grain](ctx, running, Name,
		goakt.WithLongLivedGrain(),
		goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))),
	)
	if err != nil {
		return nil, err
	}
	return &Client{
		system:   running,
		identity: identity,
	}, nil
}

// Describe returns a detached view; an in-flight write may still be applying.
func (endpoint *Client) Describe(ctx context.Context) (settings.Snapshot, error) {
	return endpoint.call(ctx, Describe{})
}

// UpdateProvider replaces one complete named configuration.
func (endpoint *Client) UpdateProvider(ctx context.Context, name string, expected int64, configuration settings.ProviderConfig) (settings.Snapshot, error) {
	return endpoint.call(ctx, UpdateProvider{
		Context:          ctx,
		Name:             name,
		ExpectedRevision: expected,
		Config:           configuration.Clone(),
	})
}

// ResetProvider clears the complete named override.
func (endpoint *Client) ResetProvider(ctx context.Context, name string, expected int64) (settings.Snapshot, error) {
	return endpoint.call(ctx, ResetProvider{
		Context:          ctx,
		Name:             name,
		ExpectedRevision: expected,
	})
}

// UpdatePrompt replaces one complete named configuration.
func (endpoint *Client) UpdatePrompt(ctx context.Context, name string, expected int64, configuration settings.PromptConfig) (settings.Snapshot, error) {
	return endpoint.call(ctx, UpdatePrompt{
		Context:          ctx,
		Name:             name,
		ExpectedRevision: expected,
		Config:           configuration.Clone(),
	})
}

// ResetPrompt clears the complete named override.
func (endpoint *Client) ResetPrompt(ctx context.Context, name string, expected int64) (settings.Snapshot, error) {
	return endpoint.call(ctx, ResetPrompt{
		Context:          ctx,
		Name:             name,
		ExpectedRevision: expected,
	})
}

// UpdateMCP replaces one complete named configuration.
func (endpoint *Client) UpdateMCP(ctx context.Context, name string, expected int64, configuration settings.MCPConfig) (settings.Snapshot, error) {
	return endpoint.call(ctx, UpdateMCP{
		Context:          ctx,
		Name:             name,
		ExpectedRevision: expected,
		Config:           configuration.Clone(),
	})
}

// ResetMCP clears the complete named override.
func (endpoint *Client) ResetMCP(ctx context.Context, name string, expected int64) (settings.Snapshot, error) {
	return endpoint.call(ctx, ResetMCP{
		Context:          ctx,
		Name:             name,
		ExpectedRevision: expected,
	})
}

// UpdateTool replaces one complete tool configuration.
func (endpoint *Client) UpdateTool(ctx context.Context, name string, expected int64, configuration tools.Config) (settings.Snapshot, error) {
	return endpoint.call(ctx, UpdateTool{
		Context:          ctx,
		Name:             name,
		ExpectedRevision: expected,
		Config:           configuration.Clone(),
	})
}

// ResetTool clears one complete tool override.
func (endpoint *Client) ResetTool(ctx context.Context, name string, expected int64) (settings.Snapshot, error) {
	return endpoint.call(ctx, ResetTool{
		Context:          ctx,
		Name:             name,
		ExpectedRevision: expected,
	})
}

func (endpoint *Client) call(ctx context.Context, message any) (settings.Snapshot, error) {
	if ctx == nil {
		return settings.Snapshot{}, settings.ErrInvalid
	}
	payload, err := endpoint.system.AskGrain(ctx, endpoint.identity, message, time.Duration(1<<63-1))
	if err != nil {
		return settings.Snapshot{}, err
	}
	result, valid := payload.(Result)
	if !valid {
		return settings.Snapshot{}, ErrUnavailable
	}
	return result.Snapshot, result.Err
}

var _ settings.Settings = (*Client)(nil)

// UpdateAgent persists a complete configuration under its own name.
func (endpoint *Client) UpdateAgent(ctx context.Context, name string, expected int64, configuration settings.Values) (settings.Snapshot, error) {
	return endpoint.call(ctx, UpdateAgent{
		Context:          ctx,
		Name:             name,
		ExpectedRevision: expected,
		Config:           configuration.Clone(),
	})
}

// UpdateCompanion replaces one complete named configuration.
func (endpoint *Client) UpdateCompanion(ctx context.Context, name string, expected int64, configuration companion.Config) (settings.Snapshot, error) {
	return endpoint.call(ctx, UpdateCompanion{
		Context:          ctx,
		Name:             name,
		ExpectedRevision: expected,
		Config:           configuration.Clone(),
	})
}

// ResetCompanion clears the complete named override.
func (endpoint *Client) ResetCompanion(ctx context.Context, name string, expected int64) (settings.Snapshot, error) {
	return endpoint.call(ctx, ResetCompanion{
		Context:          ctx,
		Name:             name,
		ExpectedRevision: expected,
	})
}
