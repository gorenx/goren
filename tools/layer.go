package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/gorenx/goren/llm"
)

// ErrToolLayerInactive reports that a ToolLayer has not been activated or was
// already closed. It is a Tools lifecycle error, not a Plugin Runtime error.
var ErrToolLayerInactive = errors.New("tools: ToolLayer is inactive")

// ToolLayer owns a local Tool catalog and execution policy registrations.
// Callers must serialize policy registration, removal, Close, and Prepare admission.
// Prepared executions retain their policy snapshot independently of later changes.
type ToolLayer struct {
	registry      *registry
	mutationMutex sync.Mutex
	toolSetMutex  sync.Mutex
	toolSets      map[string]*toolSetState
	runtimeMutex  sync.RWMutex
	runtime       *executionRuntime
	pipeline      *pipeline
	executions    map[RegistrationID]ExecuteMiddleware
	observers     map[RegistrationID]ResultObserver
	registrations []RegistrationID
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
	eventContext, unlock, err := owner.beginMutation(requestContext)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if err := owner.requireActive(); err != nil {
		return nil, err
	}
	if err := owner.registry.addTool(entry); err != nil {
		return nil, err
	}
	if err := owner.publishChanged(eventContext); err != nil {
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
	eventContext, unlock, err := owner.beginMutation(requestContext)
	if err != nil {
		return false, err
	}
	defer unlock()
	found := owner.registry.removeTool(entry.registrationName, entry)
	if !found {
		return true, nil
	}
	return true, owner.publishChanged(eventContext)
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

// Get returns the Tool definition visible from this tool layer.
func (owner *ToolLayer) Get(name string) (ToolDefinition, bool) {
	return owner.registry.lookupDefinition(name)
}

// Schemas returns the model-facing schemas visible from this tool layer.
func (owner *ToolLayer) Schemas() []llm.ToolSchema {
	return owner.registry.schemas()
}

// ExecutionMode classifies one pending call through this tool layer.
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

// CaptureDefinitions returns a detached view of visible schemas and known names.
func (owner *ToolLayer) CaptureDefinitions(ctx context.Context) (Definitions, error) {
	if err := validateMutationContext(ctx); err != nil {
		return Definitions{}, err
	}
	if err := owner.requireActive(); err != nil {
		return Definitions{}, err
	}
	return owner.registry.definitions(), nil
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
	if owner == nil || owner.pipeline == nil {
		return errors.New("tools: ToolLayer effects are unavailable")
	}
	return owner.pipeline.PublishChanged(requestContext)
}

// Close releases this plain Tool ToolLayer. It is safe to call repeatedly.
func (owner *ToolLayer) Close(closeContext context.Context) error {
	if owner == nil {
		return nil
	}
	owner.runtimeMutex.Lock()
	owner.runtime = nil
	owner.runtimeMutex.Unlock()
	owner.mutationMutex.Lock()
	owner.toolSetMutex.Lock()
	for _, state := range owner.toolSets {
		state.active = false
		state.entries = nil
	}
	owner.toolSets = nil
	owner.toolSetMutex.Unlock()
	owner.registry.clear()
	owner.mutationMutex.Unlock()
	owner.executions = nil
	owner.observers = nil
	owner.registrations = nil
	return nil
}

func validateMutationContext(requestContext context.Context) error {
	if requestContext == nil {
		return errors.New("tools: mutation Context is nil")
	}
	return requestContext.Err()
}

type mutationContextKey struct{}

func (owner *ToolLayer) beginMutation(
	requestContext context.Context,
) (context.Context, func(), error) {
	if err := validateMutationContext(requestContext); err != nil {
		return nil, nil, err
	}
	if activeOwner, present := requestContext.Value(mutationContextKey{}).(*ToolLayer); present && activeOwner == owner {
		return nil, nil, errors.New(
			"tools: registry mutation is not allowed from a tools/change observer",
		)
	}
	owner.mutationMutex.Lock()
	eventContext := context.WithValue(
		requestContext,
		mutationContextKey{},
		owner,
	)
	return eventContext, owner.mutationMutex.Unlock, nil
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
