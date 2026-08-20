package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/gorenx/goren/approval"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/systemprompt"
)

const promptToolProviderName = "tools:schemas"

type layeredToolRuntime interface {
	ToolRuntime
	toolLayers() []toolLayerSnapshot
}

// Service is the Tools Plugin lifecycle owner. It wires one scoped Registry
// to one execution runtime and exposes their capability interfaces without
// merging their state or responsibilities.
type Service struct {
	plugin.Base
	name            string
	root            bool
	settings        ValidatedConfig
	registry        *registry
	runtimeMutex    sync.RWMutex
	runtime         *executionRuntime
	prompts         systemprompt.PromptRegistry
	promptInstalled bool
}

// New constructs the root Tools Plugin from validated configuration.
func New(settings ValidatedConfig) *Service {
	return &Service{
		name:     PluginName,
		root:     true,
		settings: settings,
		registry: newRegistry(true),
	}
}

// NewOverlay constructs one child Tool registry and toolCall layer.
func NewOverlay() *Service {
	return &Service{
		name:     OverlayPluginName,
		registry: newRegistry(false),
	}
}

// Manifest declares the Tool capabilities, System Prompt integration, the
// optional Approval seam, and an overlay's ancestor ToolRuntime.
func (owner *Service) Manifest() plugin.Manifest {
	requiredServices := []plugin.ServiceType{
		plugin.ServiceOf[systemprompt.PromptRegistry](),
	}
	if !owner.root {
		requiredServices = append(
			requiredServices,
			plugin.ServiceOf[ToolRuntime](),
		)
	}
	return plugin.Manifest{
		Name: owner.name,
		Provides: []plugin.ServiceType{
			plugin.ServiceOf[ToolRuntime](),
			plugin.ServiceOf[ToolCatalog](),
			plugin.ServiceOf[PolicyRegistry](),
		},
		Requires: requiredServices,
		Optional: []plugin.ServiceType{
			plugin.ServiceOf[approval.Approval](),
		},
	}
}

// Apply resolves the layer dependencies, creates the execution runtime, and
// contributes the Registry's schema projection to System Prompt.
func (owner *Service) Apply(requestContext context.Context) error {
	if err := requestContext.Err(); err != nil {
		return err
	}
	if owner.root && (owner.settings.mode != PresentationNative ||
		owner.settings.maxParallelSubCalls < 1) {
		return errors.New("tools: configuration was not validated")
	}
	if !owner.root {
		parentService, err := plugin.Require[ToolRuntime](owner)
		if err != nil {
			return err
		}
		parent, matches := parentService.(layeredToolRuntime)
		if !matches {
			return errors.New(
				"tools: ancestor Service does not expose Tool layers",
			)
		}
		if err := owner.registry.attachParent(parent); err != nil {
			return err
		}
	}
	approvals, _ := plugin.Resolve[approval.Approval](owner)
	stagedRuntime := newRuntime(owner, owner.registry, approvals)
	prompts, err := plugin.Require[systemprompt.PromptRegistry](owner)
	if err != nil {
		return err
	}
	owner.prompts = prompts
	if err := prompts.AddToolProvider(
		requestContext,
		promptToolProviderName,
		owner,
	); err != nil {
		return err
	}
	owner.promptInstalled = true
	owner.runtimeMutex.Lock()
	owner.runtime = stagedRuntime
	owner.runtimeMutex.Unlock()
	return nil
}

// Dispose removes the schema provider and releases this exact layer after all
// dependent Plugins have stopped.
func (owner *Service) Dispose(closeContext context.Context) error {
	owner.runtimeMutex.Lock()
	owner.runtime = nil
	owner.runtimeMutex.Unlock()
	var disposeErr error
	if owner.promptInstalled && owner.prompts != nil {
		disposeErr = owner.prompts.RemoveToolProvider(
			closeContext,
			promptToolProviderName,
		)
	}
	owner.promptInstalled = false
	owner.prompts = nil
	owner.registry.clear()
	return disposeErr
}

// AddTool compiles and adds one definition to this exact Registry layer.
func (owner *Service) AddTool(
	requestContext context.Context,
	definition ToolDefinition,
) error {
	if err := validateMutationContext(requestContext); err != nil {
		return err
	}
	if err := owner.requireActive(); err != nil {
		return err
	}
	entry, err := owner.registry.compileTool(definition)
	if err != nil {
		return err
	}
	if err := owner.registry.addTool(entry); err != nil {
		return err
	}
	if err := owner.publishChanged(requestContext); err != nil {
		owner.registry.removeTool(entry.registrationName)
		return err
	}
	return nil
}

// RemoveTool removes one definition owned by this exact Registry layer.
func (owner *Service) RemoveTool(
	requestContext context.Context,
	name string,
) error {
	if err := validateMutation(requestContext, "tool", name); err != nil {
		return err
	}
	if err := owner.requireActive(); err != nil {
		return err
	}
	found := owner.registry.removeTool(name)
	if !found {
		return nil
	}
	return owner.publishChanged(requestContext)
}

// AddRestriction adds one named inherited-capability filter to this exact
// child Registry layer.
func (owner *Service) AddRestriction(
	requestContext context.Context,
	name string,
	restriction ToolRestriction,
) error {
	if err := validateMutation(requestContext, "restriction", name); err != nil {
		return err
	}
	if err := owner.requireActive(); err != nil {
		return err
	}
	compiled, err := owner.registry.compileRestriction(restriction)
	if err != nil {
		return err
	}
	if err := owner.registry.addRestriction(name, compiled); err != nil {
		return err
	}
	if err := owner.publishChanged(requestContext); err != nil {
		owner.registry.removeRestriction(name)
		return err
	}
	return nil
}

// RemoveRestriction removes one filter owned by this exact Registry layer.
func (owner *Service) RemoveRestriction(
	requestContext context.Context,
	name string,
) error {
	if err := validateMutation(requestContext, "restriction", name); err != nil {
		return err
	}
	if err := owner.requireActive(); err != nil {
		return err
	}
	found := owner.registry.removeRestriction(name)
	if !found {
		return nil
	}
	return owner.publishChanged(requestContext)
}

// AddGuard adds one named monotonic execution policy to this exact layer.
func (owner *Service) AddGuard(
	requestContext context.Context,
	name string,
	policy ToolGuard,
) error {
	if err := validateMutation(requestContext, "guard", name); err != nil {
		return err
	}
	owner.runtimeMutex.RLock()
	defer owner.runtimeMutex.RUnlock()
	if owner.runtime == nil {
		return plugin.ErrPluginNotActive
	}
	return owner.registry.addGuard(name, policy)
}

// RemoveGuard removes one policy owned by this exact Registry layer.
func (owner *Service) RemoveGuard(
	requestContext context.Context,
	name string,
) error {
	if err := validateMutation(requestContext, "guard", name); err != nil {
		return err
	}
	owner.runtimeMutex.RLock()
	defer owner.runtimeMutex.RUnlock()
	if owner.runtime == nil {
		return plugin.ErrPluginNotActive
	}
	owner.registry.removeGuard(name)
	return nil
}

// Get returns the Tool definition visible from this Service layer.
func (owner *Service) Get(name string) (ToolDefinition, bool) {
	return owner.registry.lookupDefinition(name)
}

// Schemas returns the model-facing schemas visible from this Service layer.
func (owner *Service) Schemas() []llm.ToolSchema {
	return owner.registry.schemas()
}

// ExecutionMode classifies one pending call through this Service layer.
func (owner *Service) ExecutionMode(input ToolExecutionInput) ToolExecutionMode {
	return owner.registry.executionMode(input)
}

// Execute runs one Tool call through this layer's execution runtime.
func (owner *Service) Execute(
	requestContext context.Context,
	input ToolExecutionInput,
) ToolExecutionResult {
	stagedRuntime := owner.executionRuntime()
	if stagedRuntime == nil {
		return errorResult(plugin.ErrPluginNotActive)
	}
	return stagedRuntime.Execute(requestContext, input)
}

// Scheduler returns the focused staged execution capability used by Agent Loop.
func (owner *Service) Scheduler() ToolExecutionScheduler {
	stagedRuntime := owner.executionRuntime()
	if stagedRuntime == nil {
		return nil
	}
	return stagedRuntime
}

// ResolveTools implements System Prompt's model-facing ToolProvider contract.
func (owner *Service) ResolveTools(
	requestContext context.Context,
	_ systemprompt.AssembleContext,
) (systemprompt.ToolProviderResult, error) {
	if err := requestContext.Err(); err != nil {
		return systemprompt.ToolProviderResult{}, err
	}
	return owner.registry.promptTools(), nil
}

func (owner *Service) toolLayers() []toolLayerSnapshot {
	return owner.registry.toolLayers()
}

func (owner *Service) executionRuntime() *executionRuntime {
	owner.runtimeMutex.RLock()
	stagedRuntime := owner.runtime
	owner.runtimeMutex.RUnlock()
	return stagedRuntime
}

func (owner *Service) requireActive() error {
	if owner.executionRuntime() == nil {
		return plugin.ErrPluginNotActive
	}
	return nil
}

func (owner *Service) publishChanged(requestContext context.Context) error {
	return plugin.Publish(requestContext, owner, RegistryChanged{})
}

func validateMutationContext(requestContext context.Context) error {
	if requestContext == nil {
		return errors.New("tools: mutation Context is nil")
	}
	return requestContext.Err()
}

func validateMutation(
	requestContext context.Context,
	kind string,
	name string,
) error {
	if err := validateMutationContext(requestContext); err != nil {
		return err
	}
	if strings.TrimSpace(name) == "" || name != strings.TrimSpace(name) {
		return fmt.Errorf(
			"tools: %s name must be non-empty and trimmed",
			kind,
		)
	}
	return nil
}
