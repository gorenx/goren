// Package tool implements the provider-bound model delegation Consumer.
package tool

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

// Plugin mirrors one selected Provider into a model-facing Tool definition.
type Plugin struct {
	plugin.Base
	mutex         sync.Mutex
	settings      Settings
	providers     subagent.ProviderRegistry
	oneShots      subagent.OneShotService
	continuations subagent.ContinuableService
	toolCatalog   tools.ToolCatalog
	prompts       systemprompt.PromptRegistry
	toolHandle    *tools.ToolHandle
	promptHandle  *systemprompt.PromptHandle
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

// Manifest declares use-case Services and Provider lifecycle observation.
func (owner *Plugin) Manifest() plugin.Manifest {
	required := []plugin.ServiceType{
		plugin.ServiceOf[subagent.ProviderRegistry](),
		plugin.ServiceOf[subagent.OneShotService](),
		plugin.ServiceOf[tools.ToolCatalog](),
		plugin.ServiceOf[systemprompt.PromptRegistry](),
	}
	if owner.settings.BackgroundMode == BackgroundContinuable {
		required = append(
			required,
			plugin.ServiceOf[subagent.ContinuableService](),
		)
	}
	return plugin.Manifest{
		Name:     PluginName,
		Requires: required,
		Events: []plugin.EventSubscription{
			plugin.EventOf[subagent.ProviderAdded](),
			plugin.EventOf[subagent.ProviderRemoved](),
		},
	}
}

// Apply resolves Consumers and mounts immediately when the Provider is present.
func (owner *Plugin) Apply(requestContext context.Context) error {
	providers, requireErr := plugin.Require[subagent.ProviderRegistry](owner)
	if requireErr != nil {
		return requireErr
	}
	oneShots, requireErr := plugin.Require[subagent.OneShotService](owner)
	if requireErr != nil {
		return requireErr
	}
	catalog, requireErr := plugin.Require[tools.ToolCatalog](owner)
	if requireErr != nil {
		return requireErr
	}
	prompts, requireErr := plugin.Require[systemprompt.PromptRegistry](owner)
	if requireErr != nil {
		return requireErr
	}
	var continuations subagent.ContinuableService
	if owner.settings.BackgroundMode == BackgroundContinuable {
		continuations, requireErr = plugin.Require[subagent.ContinuableService](owner)
		if requireErr != nil {
			return requireErr
		}
	}
	owner.mutex.Lock()
	owner.providers = providers
	owner.oneShots = oneShots
	owner.continuations = continuations
	owner.toolCatalog = catalog
	owner.prompts = prompts
	candidate, found := providers.GetProvider(owner.settings.Provider)
	var mountErr error
	if found {
		mountErr = owner.mount(requestContext, candidate)
	}
	owner.mutex.Unlock()
	return mountErr
}

// ObserveEvent follows only the configured Provider registration lifecycle.
func (owner *Plugin) ObserveEvent(
	requestContext context.Context,
	fact plugin.Event,
) error {
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	switch notice := fact.(type) {
	case subagent.ProviderAdded:
		if notice.Provider.Name() != owner.settings.Provider || owner.toolHandle != nil {
			return nil
		}
		return owner.mount(requestContext, notice.Provider)
	case subagent.ProviderRemoved:
		if notice.Name != owner.settings.Provider {
			return nil
		}
		return owner.unmount(context.WithoutCancel(requestContext))
	default:
		return nil
	}
}

// Dispose removes model-visible effects before releasing Service references.
func (owner *Plugin) Dispose(closeContext context.Context) error {
	if closeContext == nil {
		closeContext = context.Background()
	}
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	unmountErr := owner.unmount(context.WithoutCancel(closeContext))
	owner.providers = nil
	owner.oneShots = nil
	owner.continuations = nil
	owner.toolCatalog = nil
	owner.prompts = nil
	return unmountErr
}

func (owner *Plugin) mount(
	requestContext context.Context,
	provider subagent.Provider,
) error {
	if capabilityErr := owner.validateCapabilities(provider); capabilityErr != nil {
		return capabilityErr
	}
	toolDefinition := owner.definition(provider)
	toolHandle, addErr := owner.toolCatalog.AddTool(
		requestContext,
		toolDefinition,
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

func (owner *Plugin) validateCapabilities(provider subagent.Provider) error {
	capabilities := provider.Capabilities()
	if owner.settings.MaxDepth != nil && !capabilities.DepthLimit {
		return fmt.Errorf(
			"subagent tool: provider %q cannot enforce maxDepth",
			provider.Name(),
		)
	}
	if owner.settings.Persona != nil && !capabilities.Persona {
		return fmt.Errorf(
			"subagent tool: provider %q does not support persona",
			provider.Name(),
		)
	}
	if owner.settings.ToolFilter != nil && !capabilities.ToolFilter {
		return fmt.Errorf(
			"subagent tool: provider %q does not support toolFilter",
			provider.Name(),
		)
	}
	if owner.settings.BackgroundMode == BackgroundContinuable {
		if _, supported := provider.(subagent.ContinuableProvider); !supported {
			return fmt.Errorf(
				"subagent tool: provider %q does not support backgroundMode continuable",
				provider.Name(),
			)
		}
	}
	return nil
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
