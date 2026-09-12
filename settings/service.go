package settings

import (
	"context"
	"errors"
	"reflect"
	"time"

	"github.com/gorenx/goren/tools"
)

// Service owns configuration provenance and conditional durable submissions.
// Its Actor serializes all access, including asynchronous work; callers
// may retain only detached Snapshot values while an operation is running.
type Service struct {
	// providers captures the immutable configuration used to start this process.
	providers   map[string]ProviderConfig
	baseline    Directory
	state       State
	target      Directory
	storage     Store
	connections MCPValidator
	toolsets    ToolValidator
}

// NewService restores the latest saved configuration. Construction does not
// read storage or apply configuration to running capabilities.
func NewService(
	baseline Directory,
	initial State,
	storage Store,
	connections MCPValidator,
	toolsets ToolValidator,
) (*Service, error) {
	if storage == nil ||
		connections == nil ||
		toolsets == nil {
		return nil, ErrInvalid
	}
	if err := initial.Validate(); err != nil {
		return nil, err
	}
	resolved, err := Resolve(baseline, initial.User)
	if err != nil {
		return nil, err
	}
	return &Service{
		providers:   resolved.Clone().Providers,
		baseline:    baseline.Clone(),
		state:       initial.Clone(),
		target:      resolved,
		storage:     storage,
		connections: connections,
		toolsets:    toolsets,
	}, nil
}

// Describe implements the domain query, including caller cancellation.
func (owner *Service) Describe(ctx context.Context) (Snapshot, error) {
	if ctx == nil {
		return Snapshot{}, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	return owner.Snapshot(), nil
}

// Snapshot captures the serialized owner's query projection without I/O.
// Actor tasks and mailbox code must not call it concurrently with a mutation.
func (owner *Service) Snapshot() Snapshot {
	pending := make(map[string]bool)
	for name, configuration := range owner.target.Providers {
		pending[name] = !reflect.DeepEqual(owner.providers[name], configuration)
	}
	return Snapshot{
		PendingRestart: pending,
		Base:           owner.baseline.Clone(),
		State:          owner.state.Clone(),
		Target:         owner.target.Clone(),
	}
}

// UpdateAgent saves a complete named Agent configuration. It does not start an
// Agent or change an active Turn; the consumer applies the saved selection.
func (owner *Service) UpdateAgent(ctx context.Context, name string, expected int64, configuration Values) (Snapshot, error) {
	next := owner.state.User.Clone()
	if next.Agents == nil {
		next.Agents = make(map[string]Values)
	}
	next.Agents[name] = configuration.Clone()
	return owner.update(ctx, Group{
		Section: AgentSection,
		Name:    name,
	}, expected, next)
}

// UpdateTool replaces complete editable flags of an exposed tool.
func (owner *Service) UpdateTool(ctx context.Context, name string, expected int64, configuration tools.Config) (Snapshot, error) {
	next := owner.state.User.Clone()
	if next.Tools == nil {
		next.Tools = make(map[string]tools.Config)
	}
	next.Tools[name] = configuration.Clone()
	return owner.update(ctx, Group{
		Section: ToolSection,
		Name:    name,
	}, expected, next)
}

// UpdateProvider replaces a complete installed Provider group.
func (owner *Service) UpdateProvider(ctx context.Context, name string, expected int64, configuration ProviderConfig) (Snapshot, error) {
	next := owner.state.User.Clone()
	if next.Providers == nil {
		next.Providers = make(map[string]ProviderConfig)
	}
	next.Providers[name] = configuration.Clone()
	return owner.update(ctx, Group{
		Section: ProviderSection,
		Name:    name,
	}, expected, next)
}

// UpdatePrompt replaces a complete installed Prompt group.
func (owner *Service) UpdatePrompt(ctx context.Context, name string, expected int64, configuration PromptConfig) (Snapshot, error) {
	next := owner.state.User.Clone()
	if next.Prompts == nil {
		next.Prompts = make(map[string]PromptConfig)
	}
	next.Prompts[name] = configuration.Clone()
	return owner.update(ctx, Group{
		Section: PromptSection,
		Name:    name,
	}, expected, next)
}

// UpdateMCP replaces a complete named connection without merging fields.
func (owner *Service) UpdateMCP(ctx context.Context, name string, expected int64, configuration MCPConfig) (Snapshot, error) {
	next := owner.state.User.Clone()
	if next.MCP == nil {
		next.MCP = make(map[string]MCPConfig)
	}
	next.MCP[name] = configuration.Clone()
	return owner.update(ctx, Group{
		Section: MCPSection,
		Name:    name,
	}, expected, next)
}

func (owner *Service) update(ctx context.Context, identifier Group, expected int64, overrides Directory) (Snapshot, error) {
	if ctx == nil || !identifier.Valid() {
		return owner.Snapshot(), ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return owner.Snapshot(), err
	}
	if owner.state.Version(identifier) != expected {
		return owner.Snapshot(), ErrConflict
	}
	return owner.commit(ctx, identifier, overrides)
}

func (owner *Service) commit(ctx context.Context, identifier Group, overrides Directory) (Snapshot, error) {
	if ctx == nil {
		return owner.Snapshot(), ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return owner.Snapshot(), err
	}
	resolved, err := Resolve(owner.baseline, overrides)
	if err != nil {
		return owner.Snapshot(), err
	}
	if valuesEqual(owner.state.User, overrides) {
		return owner.Snapshot(), nil
	}
	if identifier.Section == MCPSection {
		if err := owner.connections.ValidateMCP(ctx, resolved.MCPConnections()); err != nil {
			return owner.Snapshot(), err
		}
	}
	if identifier.Section == ToolSection {
		if err := owner.toolsets.ValidateTool(ctx, identifier.Name); err != nil {
			return owner.Snapshot(), err
		}
	}
	if owner.state.Revision == maximumRevision {
		return owner.Snapshot(), ErrInvalid
	}
	next := owner.state.Clone()
	next.Revision++
	next.User = overrides.Clone()
	if next.Versions == nil {
		next.Versions = make(map[Group]int64)
	}
	next.Versions[identifier] = next.Revision
	err = owner.storage.Commit(ctx, ChangeSet{
		ExpectedRevision: owner.state.Revision,
		Next:             next,
	})
	if err != nil {
		if errors.Is(err, ErrUncertain) {
			verification, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			stored, readErr := owner.storage.Load(verification)
			cancel()
			if readErr != nil || !reflect.DeepEqual(stored, next) {
				return owner.Snapshot(), ErrUncertain
			}
		} else {
			if errors.Is(err, ErrConflict) {
				stored, readErr := owner.storage.Load(ctx)
				if readErr != nil || stored.Validate() != nil {
					return owner.Snapshot(), ErrStorage
				}
				latest, resolveErr := Resolve(owner.baseline, stored.User)
				if resolveErr != nil {
					return owner.Snapshot(), ErrStorage
				}
				owner.state = stored.Clone()
				owner.target = latest

			}
			return owner.Snapshot(), err
		}
	}
	owner.state = next
	owner.target = resolved
	return owner.Snapshot(), nil
}

var _ Settings = (*Service)(nil)
