package companionactor

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"

	"github.com/gorenx/goren/agent"
	agentregistry "github.com/gorenx/goren/agent/registry"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	"github.com/gorenx/goren/settings"
	companions "github.com/gorenx/goren/subagent/companion"
	"github.com/gorenx/goren/subagent/tools/report"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/tools"
	toolregistry "github.com/gorenx/goren/tools/registry"
	catalogactor "github.com/gorenx/goren/tools/registry/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Creator prepares named configuration and enters ordinary child creation.
// Composition supplies immutable borrowed capabilities before installing it.
// Registry retains every admitted lifetime and its cleanup responsibility.
type Creator struct {
	// System carries requests; Creator owns no Actor or transport.
	System goakt.ActorSystem
	// Registry owns parent admission, child initialization and resource release.
	Registry *goakt.GrainIdentity
	// Tools provides installed schemas and execution policies, not live MCP tools.
	Tools *goakt.GrainIdentity
	// Model provides the parent's currently adopted model selection for explicit inheritance.
	Model *goakt.GrainIdentity
	// Settings supplies the child's already-resolved independent configuration.
	Settings settings.Settings
	// Storage distinguishes first creation from restoration of the same Session.
	Storage sessionactor.Storage
}

// Start settles any submitted creation even when preparation is later cancelled.
func (owner *Creator) Start(
	ctx context.Context,
	parent, child agentregistry.Creation,
	configuration string,
) (agentregistry.Ready, error) {
	if owner == nil || ctx == nil || owner.System == nil || owner.Registry == nil ||
		owner.Tools == nil || owner.Settings == nil || owner.Storage == nil || configuration == "" {
		return agentregistry.Ready{}, companions.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return agentregistry.Ready{}, err
	}
	current, err := ask[agentregistry.Ready](ctx, owner.System, owner.Registry, agentregistry.FindAgent{
		AgentID: parent.AgentID,
	})
	if err != nil || current.Err != nil {
		return agentregistry.Ready{}, errors.Join(err, current.Err)
	}
	if current.Creation != parent || current.Registration.Role == agent.CompanionRole {
		return agentregistry.Ready{}, agentregistry.ErrCreation
	}
	view, err := owner.Settings.Describe(ctx)
	if err != nil {
		return agentregistry.Ready{}, err
	}
	initial, found := view.Target.Agents[configuration]
	if !found {
		return agentregistry.Ready{}, fmt.Errorf("%w: named Agent configuration is absent", companions.ErrInvalid)
	}
	initial = initial.Clone()
	selection, err := owner.selectTools(ctx, parent, initial)
	if err != nil {
		return agentregistry.Ready{}, err
	}
	stored, err := owner.Storage.Load(ctx, child.AgentID)
	if err != nil {
		return agentregistry.Ready{}, err
	}
	prompt := initial.Prompts["default"].Prompt()
	components := make([]*goakt.GrainIdentity, 0, len(current.Components))
	for _, entry := range current.Components {
		if entry.Owner.Name() != InstallerName {
			components = append(components, entry.Owner)
		}
	}
	input := agentregistry.CreateChildAgent{
		Role:           agent.CompanionRole,
		Parent:         parent,
		Child:          child,
		Restore:        stored != nil,
		AgentOptions:   &initial.Model,
		Prompt:         &prompt,
		Tools:          &selection,
		PromptSections: []systemprompt.PromptSection{report.Section()},
		Components:     components,
	}
	if !input.Restore {
		input.Options.Metadata.Origin = session.OriginSubagent
	}
	if err := ctx.Err(); err != nil {
		return agentregistry.Ready{}, err
	}
	// Once submitted, this request must settle before Steward may close or retry
	// the reserved lifetime. Parent admission is checked again by Registry.
	settlement := context.WithoutCancel(ctx)
	ready, err := ask[agentregistry.Ready](settlement, owner.System, owner.Registry, input)
	if err != nil || ready.Err != nil {
		return ready, errors.Join(err, ready.Err)
	}
	if ready.Creation != child {
		return ready, agentregistry.ErrCreation
	}
	return ready, nil
}

func (owner *Creator) selectTools(
	ctx context.Context,
	parent agentregistry.Creation,
	initial settings.Values,
) (toolregistry.Catalog, error) {
	available, err := ask[catalogactor.Configuration](ctx, owner.System, owner.Tools, catalogactor.ReadConfiguration{})
	if err != nil {
		return toolregistry.Catalog{}, err
	}
	parentTools, err := ask[catalogactor.Configuration](ctx, owner.System, owner.Tools, catalogactor.ReadConfiguration{
		SessionID: parent.AgentID,
	})
	if err != nil {
		return toolregistry.Catalog{}, err
	}
	selected := toolregistry.Catalog{
		Schemas:     []llm.ToolSchema{report.Schema()},
		MCP:         initial.MCPConnections(),
		Policies:    make(map[string]toolregistry.Policy),
		ProviderIDs: make(map[string]tools.ProviderID),
	}
	names := make([]string, 0, len(initial.Tools))
	for name := range initial.Tools {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if name == report.Schema().Name {
			return toolregistry.Catalog{}, fmt.Errorf("%w: report tool name is reserved", companions.ErrInvalid)
		}
		source := parentTools.Value.Global
		index := slices.IndexFunc(source.Schemas, func(entry llm.ToolSchema) bool {
			return entry.Name == name
		})
		if index < 0 {
			source = available.Value.Global
			index = slices.IndexFunc(source.Schemas, func(entry llm.ToolSchema) bool {
				return entry.Name == name
			})
		}
		if index < 0 && len(selected.MCP) == 0 {
			return toolregistry.Catalog{}, fmt.Errorf("%w: tool %q is not installed", companions.ErrInvalid, name)
		}
		policy := source.Policies[name]
		if index >= 0 {
			definition := source.Schemas[index]
			definition.Version = 0
			selected.Schemas = append(selected.Schemas, definition)
			if identifier := source.ProviderIDs[name]; identifier != "" {
				selected.ProviderIDs[name] = identifier
			}
		}
		policy.Enabled = initial.Tools[name].Enabled
		policy.Visible = initial.Tools[name].Visible
		selected.Policies[name] = policy
	}
	return selected.Clone(), nil
}

var _ ChildCreator = (*Creator)(nil)
