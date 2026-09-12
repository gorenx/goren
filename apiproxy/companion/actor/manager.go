// Package companionadapter connects the Companion API to admitted Steward clients.
package companionadapter

import (
	"context"

	agentregistry "github.com/gorenx/goren/agent/registry"
	companionapi "github.com/gorenx/goren/apiproxy/companion"
	"github.com/gorenx/goren/settings"
	companions "github.com/gorenx/goren/subagent/companion"
	companionactor "github.com/gorenx/goren/subagent/companion/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Resolver freezes explicitly selected parent values before Steward persists them.
type Resolver interface {
	Resolve(context.Context, agentregistry.Creation, companions.Config, settings.Values) (settings.Values, error)
}

// Manager borrows current Agent routes and Settings; it owns no child state.
type Manager struct {
	system   goakt.ActorSystem
	registry *goakt.GrainIdentity
	settings settings.Settings
	resolver Resolver
}

// NewManager binds the existing owners without creating a parent or child.
func NewManager(
	system goakt.ActorSystem,
	registry *goakt.GrainIdentity,
	configuration settings.Settings,
	resolver Resolver,
) (*Manager, error) {
	if system == nil || registry == nil || configuration == nil || resolver == nil {
		return nil, companions.ErrInvalid
	}
	return &Manager{
		system:   system,
		registry: registry,
		settings: configuration,
		resolver: resolver,
	}, nil
}

// List reads the parent's actual installation and current relationship views.
func (source *Manager) List(ctx context.Context, input companionapi.ListRequest) (companionapi.Listing, error) {
	sender, err := companionactor.OpenClient(ctx, source.system, source.registry, input.ParentSessionID)
	if err != nil {
		return companionapi.Listing{}, err
	}
	status, err := sender.Status(ctx)
	if err != nil {
		return companionapi.Listing{}, err
	}
	result := companionapi.Listing{
		Phase:   string(status.Phase),
		Failure: status.Err != nil,
		Items:   []companionapi.View{},
	}
	if status.Phase == companionactor.Starting || status.Phase == companionactor.StartFailed {
		return result, nil
	}
	views, err := sender.List(ctx)
	if err != nil {
		return companionapi.Listing{}, err
	}
	for _, view := range views {
		result.Items = append(result.Items, project(view))
	}
	return result, nil
}

// Create resolves a complete policy before entering the Steward persistence path.
func (source *Manager) Create(ctx context.Context, input companionapi.CreateRequest) (companionapi.View, error) {
	sender, err := companionactor.OpenClient(ctx, source.system, source.registry, input.ParentSessionID)
	if err != nil {
		return companionapi.View{}, err
	}
	snapshot, err := source.settings.Describe(ctx)
	if err != nil {
		return companionapi.View{}, err
	}
	policy := companions.Config{}
	revision := int64(0)
	if input.Config == nil {
		var found bool
		policy, found = snapshot.Target.Companions[input.Name]
		if !found {
			return companionapi.View{}, companionapi.ErrNotFound
		}
		revision = snapshot.State.Version(settings.Group{
			Section: settings.CompanionSection,
			Name:    input.Name,
		})
	} else {
		policy = input.Config.Clone()
	}
	if err := policy.Validate(); err != nil {
		return companionapi.View{}, err
	}
	initial := settings.Values{}
	if policy.Configuration != "" {
		var found bool
		initial, found = snapshot.Target.Agents[policy.Configuration]
		if !found {
			return companionapi.View{}, companionapi.ErrNotFound
		}
	}
	resolved, err := source.resolver.Resolve(ctx, sender.Creation(), policy, initial.Clone())
	if err != nil {
		return companionapi.View{}, err
	}
	view, err := sender.Create(ctx, companionactor.Create{
		Name:           input.Name,
		SourceRevision: revision,
		Enabled:        policy.Enabled,
		Initial:        resolved,
	})
	return project(view), err
}

// Configure changes the saved target without waiting for the child transition.
func (source *Manager) Configure(ctx context.Context, input companionapi.ConfigureRequest) (companionapi.View, error) {
	sender, err := companionactor.OpenClient(ctx, source.system, source.registry, input.ParentSessionID)
	if err != nil {
		return companionapi.View{}, err
	}
	view, err := sender.Configure(ctx, companionactor.Configure{
		Name:             input.Name,
		ExpectedRevision: input.ExpectedRevision,
		Enabled:          input.Enabled,
	})
	return project(view), err
}

// Remove records a tombstone through the parent Steward.
func (source *Manager) Remove(ctx context.Context, input companionapi.RemoveRequest) (companionapi.View, error) {
	sender, err := companionactor.OpenClient(ctx, source.system, source.registry, input.ParentSessionID)
	if err != nil {
		return companionapi.View{}, err
	}
	view, err := sender.Remove(ctx, companionactor.Remove{
		Name:             input.Name,
		ExpectedRevision: input.ExpectedRevision,
	})
	return project(view), err
}

// Restart closes the observed creation before recovering its existing Session.
func (source *Manager) Restart(ctx context.Context, input companionapi.RestartRequest) (companionapi.View, error) {
	sender, err := companionactor.OpenClient(ctx, source.system, source.registry, input.ParentSessionID)
	if err != nil {
		return companionapi.View{}, err
	}
	view, err := sender.Restart(ctx, companionactor.Restart{
		Name:               input.Name,
		ExpectedRevision:   input.ExpectedRevision,
		ExpectedCreationID: input.ExpectedCreationID,
	})
	return project(view), err
}

func project(input companionactor.View) companionapi.View {
	return companionapi.View{
		ParentSessionID: input.Companion.ParentSessionID,
		Name:            input.Companion.Name,
		ChildSessionID:  input.Companion.ChildSessionID,
		Revision:        input.Companion.Revision,
		SourceRevision:  input.Companion.SourceRevision,
		CreationID:      input.Creation.CreationID,
		Enabled:         input.Companion.Enabled,
		Removed:         input.Companion.Removed,
		Persisted:       input.Persisted,
		Phase:           string(input.Phase),
		Failure:         input.Err != nil,
	}
}

var _ companionapi.Manager = (*Manager)(nil)
var _ Resolver = (*companionactor.Creator)(nil)
