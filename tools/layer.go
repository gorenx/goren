package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/systemprompt"
)

const promptToolProviderName = "tools:schemas"

// ErrToolLayerInactive reports that a ToolLayer has not been activated or was
// already closed. It is a Tools lifecycle error, not a Plugin Runtime error.
var ErrToolLayerInactive = errors.New("tools: ToolLayer is inactive")

type layeredToolRuntime interface {
	ToolRuntime
	toolLayers() []toolLayerSnapshot
}

// ToolLayerFactory creates plain Agent-local Tool Layers without mounting child
// Plugins. Each ToolLayer inherits this provider's root catalog and policies.
type ToolLayerFactory interface {
	NewLayer(
		context.Context,
		systemprompt.PromptRegistry,
	) (*ToolLayer, error)
}

type layerEffects interface {
	ResolvePreExecute(
		context.Context,
		PreExecuteRequest,
	) (PreExecuteOutcome, error)
	ResolveExecute(
		context.Context,
		ExecuteRequest,
		ExecuteAction,
	) (ExecuteOutcome, error)
	ResolvePostExecute(
		context.Context,
		PostExecuteRequest,
	) (PostExecuteOutcome, error)
	PublishCompleted(context.Context, ExecutionCompleted) error
	PublishChanged(context.Context) error
}

// ToolLayer owns one Tool catalog, policy, and execution layer independently
// from Plugin lifecycle. It has only provider-facing parent and effect ports.
type ToolLayer struct {
	registry       *registry
	runtimeMutex   sync.RWMutex
	runtime        *executionRuntime
	prompt         *systemprompt.PromptHandle
	effects        layerEffects
	observerMutex  sync.RWMutex
	observers      []*ResultObserverHandle
	executionMutex sync.RWMutex
	executions     []*ExecuteMiddlewareHandle
}

// AddTool compiles and adds one definition to this exact Registry layer.
func (owner *ToolLayer) AddTool(
	requestContext context.Context,
	definition ToolDefinition,
) (*ToolHandle, error) {
	if err := validateMutationContext(requestContext); err != nil {
		return nil, err
	}
	if err := owner.requireActive(); err != nil {
		return nil, err
	}
	entry, err := owner.registry.compileTool(definition)
	if err != nil {
		return nil, err
	}
	if err := owner.registry.addTool(entry); err != nil {
		return nil, err
	}
	if err := owner.publishChanged(requestContext); err != nil {
		owner.registry.removeTool(entry.registrationName, entry)
		return nil, err
	}
	return &ToolHandle{
		owner: owner,
		entry: entry,
	}, nil
}

func (owner *ToolLayer) unregisterTool(
	requestContext context.Context,
	entry *registeredTool,
) (bool, error) {
	if err := validateMutationContext(requestContext); err != nil {
		return false, err
	}
	found := owner.registry.removeTool(entry.registrationName, entry)
	if !found {
		return true, nil
	}
	return true, owner.publishChanged(requestContext)
}

// AddRestriction adds one named inherited-capability filter to this exact
// child Registry layer.
func (owner *ToolLayer) AddRestriction(
	requestContext context.Context,
	name string,
	restriction ToolRestriction,
) (*RestrictionHandle, error) {
	if err := validateMutation(requestContext, "restriction", name); err != nil {
		return nil, err
	}
	if err := owner.requireActive(); err != nil {
		return nil, err
	}
	compiled, err := owner.registry.compileRestriction(restriction)
	if err != nil {
		return nil, err
	}
	entry, err := owner.registry.addRestriction(name, compiled)
	if err != nil {
		return nil, err
	}
	if err := owner.publishChanged(requestContext); err != nil {
		owner.registry.removeRestriction(name, entry)
		return nil, err
	}
	return &RestrictionHandle{
		owner: owner,
		name:  name,
		entry: entry,
	}, nil
}

func (owner *ToolLayer) unregisterRestriction(
	requestContext context.Context,
	name string,
	entry *registeredRestriction,
) (bool, error) {
	if err := validateMutationContext(requestContext); err != nil {
		return false, err
	}
	found := owner.registry.removeRestriction(name, entry)
	if !found {
		return true, nil
	}
	return true, owner.publishChanged(requestContext)
}

// AddGuard adds one named monotonic execution policy to this exact layer.
func (owner *ToolLayer) AddGuard(
	requestContext context.Context,
	name string,
	policy ToolGuard,
) (*GuardHandle, error) {
	if err := validateMutation(requestContext, "guard", name); err != nil {
		return nil, err
	}
	owner.runtimeMutex.RLock()
	defer owner.runtimeMutex.RUnlock()
	if owner.runtime == nil {
		return nil, ErrToolLayerInactive
	}
	entry, err := owner.registry.addGuard(name, policy)
	if err != nil {
		return nil, err
	}
	return &GuardHandle{
		owner: owner,
		name:  name,
		entry: entry,
	}, nil
}

func (owner *ToolLayer) unregisterGuard(
	requestContext context.Context,
	name string,
	entry *registeredGuard,
) (bool, error) {
	if err := validateMutationContext(requestContext); err != nil {
		return false, err
	}
	owner.runtimeMutex.RLock()
	defer owner.runtimeMutex.RUnlock()
	owner.registry.removeGuard(name, entry)
	return true, nil
}

// Get returns the Tool definition visible from this Service layer.
func (owner *ToolLayer) Get(name string) (ToolDefinition, bool) {
	return owner.registry.lookupDefinition(name)
}

// Schemas returns the model-facing schemas visible from this Service layer.
func (owner *ToolLayer) Schemas() []llm.ToolSchema {
	return owner.registry.schemas()
}

// ExecutionMode classifies one pending call through this Service layer.
func (owner *ToolLayer) ExecutionMode(input ToolExecutionInput) ToolExecutionMode {
	return owner.registry.executionMode(input)
}

// Execute runs one Tool call through this layer's execution runtime.
func (owner *ToolLayer) Execute(
	requestContext context.Context,
	input ToolExecutionInput,
) ToolExecutionResult {
	stagedRuntime := owner.executionRuntime()
	if stagedRuntime == nil {
		return errorResult(ErrToolLayerInactive)
	}
	return stagedRuntime.Execute(requestContext, input)
}

// Scheduler returns the focused staged execution capability used by Agent Loop.
func (owner *ToolLayer) Scheduler() ToolExecutionScheduler {
	stagedRuntime := owner.executionRuntime()
	if stagedRuntime == nil {
		return nil
	}
	return stagedRuntime
}

// ResolveTools implements System Prompt's model-facing ToolProvider contract.
func (owner *ToolLayer) ResolveTools(
	requestContext context.Context,
	_ systemprompt.AssembleContext,
) (systemprompt.ToolProviderResult, error) {
	if err := requestContext.Err(); err != nil {
		return systemprompt.ToolProviderResult{}, err
	}
	return owner.registry.promptTools(), nil
}

func (owner *ToolLayer) toolLayers() []toolLayerSnapshot {
	return owner.registry.toolLayers()
}

func (owner *ToolLayer) executionRuntime() *executionRuntime {
	owner.runtimeMutex.RLock()
	stagedRuntime := owner.runtime
	owner.runtimeMutex.RUnlock()
	return stagedRuntime
}

func (owner *ToolLayer) requireActive() error {
	if owner.executionRuntime() == nil {
		return ErrToolLayerInactive
	}
	return nil
}

func (owner *ToolLayer) publishChanged(requestContext context.Context) error {
	if owner == nil || owner.effects == nil {
		return errors.New("tools: ToolLayer effects are unavailable")
	}
	return owner.effects.PublishChanged(requestContext)
}

// Close releases this plain Tool ToolLayer. It is safe to call repeatedly.
func (owner *ToolLayer) Close(closeContext context.Context) error {
	if owner == nil {
		return nil
	}
	owner.runtimeMutex.Lock()
	owner.runtime = nil
	owner.runtimeMutex.Unlock()
	var closeErr error
	if owner.prompt != nil {
		closeErr = owner.prompt.Unregister(closeContext)
		owner.prompt = nil
	}
	owner.registry.clear()
	owner.observerMutex.Lock()
	observers := owner.observers
	owner.observers = nil
	owner.observerMutex.Unlock()
	for _, observer := range observers {
		closeErr = errors.Join(closeErr, observer.Close(closeContext))
	}
	owner.executionMutex.Lock()
	executions := owner.executions
	owner.executions = nil
	owner.executionMutex.Unlock()
	for _, middlewareHandle := range executions {
		closeErr = errors.Join(closeErr, middlewareHandle.Close(closeContext))
	}
	return closeErr
}

// ObserveResults registers one ordinary observer in this exact ToolLayer.
func (owner *ToolLayer) ObserveResults(observer ResultObserver) (*ResultObserverHandle, error) {
	if owner == nil || observer == nil {
		return nil, errors.New("tools: result observer is required")
	}
	handle := &ResultObserverHandle{
		observer: observer,
		active:   true,
	}
	owner.observerMutex.Lock()
	owner.observers = append(owner.observers, handle)
	owner.observerMutex.Unlock()
	return handle, nil
}

func (owner *ToolLayer) ResolvePreExecute(
	requestContext context.Context,
	request PreExecuteRequest,
) (PreExecuteOutcome, error) {
	return owner.effects.ResolvePreExecute(requestContext, request)
}

func (owner *ToolLayer) ResolveExecute(
	requestContext context.Context,
	request ExecuteRequest,
	terminal ExecuteAction,
) (ExecuteOutcome, error) {
	owner.executionMutex.RLock()
	middlewareHandles := append(
		[]*ExecuteMiddlewareHandle(nil),
		owner.executions...,
	)
	owner.executionMutex.RUnlock()
	local := terminal
	for index := len(middlewareHandles) - 1; index >= 0; index-- {
		middleware, active := middlewareHandles[index].middlewareValue()
		if !active {
			continue
		}
		local = executeMiddlewareAction{
			middleware: middleware,
			next:       local,
		}
	}
	return owner.effects.ResolveExecute(requestContext, request, local)
}

func (owner *ToolLayer) ResolvePostExecute(
	requestContext context.Context,
	request PostExecuteRequest,
) (PostExecuteOutcome, error) {
	return owner.effects.ResolvePostExecute(requestContext, request)
}

func (owner *ToolLayer) PublishCompleted(
	requestContext context.Context,
	completed ExecutionCompleted,
) error {
	owner.observerMutex.RLock()
	observers := append([]*ResultObserverHandle(nil), owner.observers...)
	owner.observerMutex.RUnlock()
	for _, observer := range observers {
		_ = observer.observe(requestContext, completed)
	}
	return owner.effects.PublishCompleted(requestContext, completed)
}

func (owner *ToolLayer) PublishChanged(requestContext context.Context) error {
	return owner.effects.PublishChanged(requestContext)
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
