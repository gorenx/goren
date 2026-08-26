// Package delegation exposes one configured Subagent SeedBuilder through a
// model-facing delegation Tool.
package delegation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/tools"
)

const (
	// PluginName is the canonical model-facing delegation Plugin name.
	PluginName = "@deepseek-ai/dsh-tool-subagent"
	// DefaultToolName is the default model-visible delegation Tool name.
	DefaultToolName = "subagent"
)

// BackgroundMode selects the lifecycle used when a call runs in background.
type BackgroundMode string

const (
	// BackgroundOneShot selects generic Jobs collection, excluded in Goren.
	BackgroundOneShot BackgroundMode = "one-shot"
	// BackgroundContinuable selects a durable continuable child.
	BackgroundContinuable BackgroundMode = "continuable"
)

// Settings contains validated operational policy for one delegation Tool.
// Provider preserves the canonical configuration key; its value identifies a
// registered SeedBuilder, not an Agent constructor.
type Settings struct {
	Provider              string
	ToolName              string
	EnableRunInBackground bool
	BackgroundMode        BackgroundMode
	AgentOptions          *agent.Options
	Persona               *string
	ToolFilter            *tools.ToolRestriction
	MaxDepth              *int64
}

// Plugin owns only SeedBuilder-driven Tool and prompt registration lifecycle.
// Tool execution is delegated to delegationTool.
type Plugin struct {
	plugin.Base
	mutex        sync.Mutex
	settings     Settings
	builders     subagent.SeedBuilderRegistry
	delegation   *delegationTool
	toolCatalog  tools.ToolCatalog
	prompts      systemprompt.PromptRegistry
	toolHandle   *tools.ToolHandle
	promptHandle *systemprompt.PromptHandle
}

// New validates settings and constructs an inactive Tool Plugin.
func New(toolSettings Settings) (*Plugin, error) {
	if strings.TrimSpace(toolSettings.Provider) == "" ||
		toolSettings.Provider != strings.TrimSpace(toolSettings.Provider) {
		return nil, errors.New("subagent tool: provider must be non-empty and trimmed")
	}
	if strings.TrimSpace(toolSettings.ToolName) == "" ||
		toolSettings.ToolName != strings.TrimSpace(toolSettings.ToolName) {
		return nil, errors.New("subagent tool: toolName must be non-empty and trimmed")
	}
	switch toolSettings.BackgroundMode {
	case BackgroundOneShot, BackgroundContinuable:
	default:
		return nil, fmt.Errorf(
			"subagent tool: unsupported backgroundMode %q",
			toolSettings.BackgroundMode,
		)
	}
	if toolSettings.MaxDepth != nil &&
		(*toolSettings.MaxDepth < 0 || *toolSettings.MaxDepth > 1<<53-1) {
		return nil, errors.New(
			"subagent tool: maxDepth must be a non-negative safe integer",
		)
	}
	if toolSettings.ToolFilter != nil &&
		toolSettings.ToolFilter.Allow == nil && toolSettings.ToolFilter.Deny == nil {
		return nil, errors.New(
			"subagent tool: toolFilter must declare allow and/or deny",
		)
	}
	return &Plugin{
		settings: cloneSettings(toolSettings),
	}, nil
}

// Manifest declares model-facing dependencies and observes the canonical
// SeedBuilder registration events.
func (*Plugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: PluginName,
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[subagent.SeedBuilderRegistry](),
			plugin.ServiceOf[subagent.Starter](),
			plugin.ServiceOf[tools.ToolCatalog](),
			plugin.ServiceOf[systemprompt.PromptRegistry](),
		},
		Events: []plugin.EventSubscription{
			plugin.EventOf[subagent.SeedBuilderAdded](),
			plugin.EventOf[subagent.SeedBuilderRemoved](),
		},
	}
}

// Apply resolves dependencies and mounts immediately when the configured
// SeedBuilder is already present.
func (owner *Plugin) Apply(requestContext context.Context) error {
	builders, requireErr := plugin.Require[subagent.SeedBuilderRegistry](owner)
	if requireErr != nil {
		return requireErr
	}
	starter, requireErr := plugin.Require[subagent.Starter](owner)
	if requireErr != nil {
		return requireErr
	}
	toolCatalog, requireErr := plugin.Require[tools.ToolCatalog](owner)
	if requireErr != nil {
		return requireErr
	}
	prompts, requireErr := plugin.Require[systemprompt.PromptRegistry](owner)
	if requireErr != nil {
		return requireErr
	}
	delegation, constructionErr := newDelegationTool(owner.settings, starter)
	if constructionErr != nil {
		return constructionErr
	}
	owner.mutex.Lock()
	owner.builders = builders
	owner.delegation = delegation
	owner.toolCatalog = toolCatalog
	owner.prompts = prompts
	builder, found := builders.Find(owner.settings.Provider)
	var mountErr error
	if found {
		mountErr = owner.mount(requestContext, builder)
	}
	owner.mutex.Unlock()
	return mountErr
}

// ObserveEvent follows only the configured SeedBuilder registration.
func (owner *Plugin) ObserveEvent(
	requestContext context.Context,
	fact plugin.Event,
) error {
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	switch notice := fact.(type) {
	case subagent.SeedBuilderAdded:
		if notice.SeedBuilder.Name() != owner.settings.Provider ||
			owner.toolHandle != nil {
			return nil
		}
		return owner.mount(requestContext, notice.SeedBuilder)
	case subagent.SeedBuilderRemoved:
		if notice.Name != owner.settings.Provider {
			return nil
		}
		return owner.unmount(context.WithoutCancel(requestContext))
	default:
		return nil
	}
}

// Dispose removes model-visible effects before releasing capability references.
func (owner *Plugin) Dispose(closeContext context.Context) error {
	if closeContext == nil {
		closeContext = context.Background()
	}
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	unmountErr := owner.unmount(context.WithoutCancel(closeContext))
	owner.builders = nil
	owner.delegation = nil
	owner.toolCatalog = nil
	owner.prompts = nil
	return unmountErr
}

func (owner *Plugin) mount(
	requestContext context.Context,
	builder subagent.SeedBuilder,
) error {
	toolHandle, addErr := owner.toolCatalog.AddTool(
		requestContext,
		owner.delegation.definition(builder),
	)
	if addErr != nil {
		return addErr
	}
	owner.toolHandle = toolHandle
	if owner.settings.EnableRunInBackground &&
		owner.settings.BackgroundMode == BackgroundContinuable {
		promptHandle, promptErr := owner.prompts.AddSection(
			requestContext,
			systemprompt.PromptSection{
				Name:  "tool:" + owner.settings.ToolName,
				Order: 116.5,
				Text: systemprompt.StaticText(
					"Use " + owner.settings.ToolName + " in the background by default. Start independent delegations together and continue useful work while they run. Set `run_in_background: false` only when your next action depends on that subagent's result.",
				),
			},
		)
		if promptErr != nil {
			rollbackErr := owner.unmount(context.WithoutCancel(requestContext))
			return errors.Join(promptErr, rollbackErr)
		}
		owner.promptHandle = promptHandle
	}
	return nil
}

func (owner *Plugin) unmount(closeContext context.Context) error {
	var closeErr error
	if owner.promptHandle != nil {
		closeErr = errors.Join(
			closeErr,
			owner.promptHandle.Unregister(closeContext),
		)
		owner.promptHandle = nil
	}
	if owner.toolHandle != nil {
		closeErr = errors.Join(
			closeErr,
			owner.toolHandle.Unregister(closeContext),
		)
		owner.toolHandle = nil
	}
	return closeErr
}

func cloneSettings(source Settings) Settings {
	detached := source
	if source.AgentOptions != nil {
		agentOptions := *source.AgentOptions
		if source.AgentOptions.MaxTokens != nil {
			maxTokensValue := *source.AgentOptions.MaxTokens
			agentOptions.MaxTokens = &maxTokensValue
		}
		detached.AgentOptions = &agentOptions
	}
	if source.Persona != nil {
		personaText := *source.Persona
		detached.Persona = &personaText
	}
	if source.ToolFilter != nil {
		detached.ToolFilter = &tools.ToolRestriction{
			Allow: cloneStrings(source.ToolFilter.Allow),
			Deny:  cloneStrings(source.ToolFilter.Deny),
		}
	}
	if source.MaxDepth != nil {
		maxDepthValue := *source.MaxDepth
		detached.MaxDepth = &maxDepthValue
	}
	return detached
}

func cloneStrings(source []string) []string {
	if source == nil {
		return nil
	}
	detached := make([]string, len(source))
	copy(detached, source)
	return detached
}

var _ plugin.EventObserver = (*Plugin)(nil)
