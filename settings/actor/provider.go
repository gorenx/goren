package settingsactor

import (
	"context"
	"errors"
	"time"

	"github.com/gorenx/goren/settings"
	"github.com/gorenx/goren/settings/internal/sqlite"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Provider owns the Settings store and installed Grain within a borrowed
// ActorSystem. Composition serializes Open, Bind and Close and retains failed
// close responsibility; this object creates no independent runtime.
type Provider struct {
	storage  *sqlite.Adapter
	baseline settings.Directory
	stored   settings.State
	target   settings.Directory
	client   *Client
}

// Open validates persisted overrides before any consuming component is created.
func Open(ctx context.Context, path string, baseline settings.Directory) (*Provider, error) {
	if err := baseline.Validate(); err != nil {
		return nil, err
	}
	storage, err := sqlite.Open(ctx, path)
	if err != nil {
		return nil, err
	}
	stored, err := storage.Load(ctx)
	if err == nil {
		err = stored.Validate()
	}
	if err != nil {
		return nil, errors.Join(err, storage.Close())
	}
	target, err := settings.Resolve(baseline, stored.User)
	if err != nil {
		return nil, errors.Join(err, storage.Close())
	}
	return &Provider{
		storage:  storage,
		baseline: baseline.Clone(),
		stored:   stored.Clone(),
		target:   target,
	}, nil
}

// Inspect checks persisted settings without creating a database or an Actor.
func Inspect(ctx context.Context, path string, baseline settings.Directory) (settings.Directory, error) {
	stored, err := sqlite.Inspect(ctx, path)
	if err != nil {
		return settings.Directory{}, err
	}
	if err := stored.Validate(); err != nil {
		return settings.Directory{}, err
	}
	return settings.Resolve(baseline, stored.User)
}

// Target supplies isolated initial configuration to the composition root.
func (owner *Provider) Target() settings.Directory { return owner.target.Clone() }

// Bind publishes the service after the composition root applies Target.
func (owner *Provider) Bind(ctx context.Context,
	running goakt.ActorSystem,
	prompts settings.PromptUpdater,
	connections settings.MCPUpdater,
	toolsets settings.ToolUpdater,
) (*Client, error) {
	if owner.client != nil ||
		owner.storage == nil ||
		running == nil {
		return nil, ErrUnavailable
	}
	business, err := settings.NewService(owner.baseline, owner.stored, owner.storage, prompts, connections, toolsets)
	if err != nil {
		return nil, err
	}
	groups := owner.target.Groups()
	for _, identifier := range groups {
		if identifier.Section != settings.ToolSection {
			continue
		}
		if err := toolsets.ValidateTool(ctx, identifier.Name); err != nil {
			return nil, err
		}
	}
	for _, identifier := range groups {
		if identifier.Section != settings.ToolSection {
			continue
		}
		if err := toolsets.ApplyTool(ctx, identifier.Name, owner.target.Tools[identifier.Name]); err != nil {
			return nil, err
		}
	}
	endpoint, err := NewClient(ctx, running)
	if err != nil {
		return nil, err
	}
	// Retain cleanup responsibility even if the initialization reply is lost.
	owner.client = endpoint
	_, err = endpoint.call(ctx, Initialize{
		Service: business,
	})
	if err != nil {
		return nil, err
	}
	return endpoint, nil
}

// Close drains the service before releasing its store. The outer composition
// root owns ActorSystem shutdown; a drained Grain refuses all further writes.
func (owner *Provider) Close(ctx context.Context) error {
	if ctx == nil {
		return settings.ErrInvalid
	}
	if owner.client != nil {
		bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
		_, err := owner.client.call(bounded, Close{})
		cancel()
		if err != nil {
			return err
		}
		owner.client = nil
	}
	if owner.storage != nil {
		if err := owner.storage.Close(); err != nil {
			return err
		}
		owner.storage = nil
	}
	return nil
}
