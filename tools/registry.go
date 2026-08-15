package tools

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"

	"github.com/gorenx/goren/internal/jsonvalue"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/systemprompt"
)

// toolRegistry is the Tools service facade. It validates capability calls,
// binds contributions to plugin lifecycles, and delegates mutable state and
// execution to their dedicated components.
type toolRegistry struct {
	sourceScope *plugin.Scope
	store       *toolStore
	approvals   ApprovalResolver
	reporter    ResultObserverReporter
}

// New creates one scope-aware Tools registry and contributes its model-facing
// schema provider to System Prompt for the lifetime of sourceScope.
func New(
	requestContext context.Context,
	sourceScope *plugin.Scope,
	promptService systemprompt.SystemPrompt,
	approvalCapability ApprovalResolver,
	reporter ResultObserverReporter,
	settings ValidatedConfig,
) (ToolRuntime, error) {
	if sourceScope == nil {
		return nil, errors.New("tools: source scope is nil")
	}
	if promptService == nil {
		return nil, errors.New("tools: system prompt service is nil")
	}
	if settings.mode != PresentationNative || settings.maxParallelSubCalls < 1 {
		return nil, errors.New("tools: configuration was not validated")
	}
	owner := &toolRegistry{
		sourceScope: sourceScope,
		store:       newToolStore(),
		approvals:   approvalCapability,
		reporter:    reporter,
	}
	_, err := promptService.Tools(requestContext, sourceScope,
		func(_ context.Context, assemblyContext systemprompt.AssembleContext) (systemprompt.ToolProviderResult, error) {
			return owner.promptTools(assemblyContext.Scope), nil
		})
	if err != nil {
		return nil, err
	}
	return owner, nil
}

func (owner *toolRegistry) Register(requestContext context.Context, ownerScope *plugin.Scope, definition ToolDefinition) (plugin.Disposer, error) {
	if ownerScope == nil {
		return nil, errors.New("tools: registration owner scope is nil")
	}
	entry, err := compileDefinition(definition)
	if err != nil {
		return nil, err
	}
	undo, err := owner.store.addTool(ownerScope.Target(), entry)
	if err != nil {
		return nil, err
	}
	return owner.ownMutation(requestContext, ownerScope, "tools.register()", undo, true)
}

func (owner *toolRegistry) Restrict(requestContext context.Context, ownerScope *plugin.Scope, restriction ToolRestriction) (plugin.Disposer, error) {
	if ownerScope == nil {
		return nil, errors.New("tools: restriction owner scope is nil")
	}
	selectedKey := ownerScope.Target()
	if selectedKey.IsGlobal() {
		return nil, errors.New("tools: restrict requires a child scope")
	}
	if restriction.Allow == nil && restriction.Deny == nil {
		return nil, errors.New("tools: restrict requires allow and/or deny")
	}
	resolved := owner.store.view(selectedKey)
	compiled, err := compileRestriction(restriction, resolved.restrictableName)
	if err != nil {
		return nil, err
	}
	undo := owner.store.addRestriction(selectedKey, compiled)
	return owner.ownMutation(requestContext, ownerScope, "tools.restrict()", undo, true)
}

func (owner *toolRegistry) Guard(ownerScope *plugin.Scope, policy ToolGuard) (plugin.Disposer, error) {
	if ownerScope == nil {
		return nil, errors.New("tools: guard owner scope is nil")
	}
	if policy == nil {
		return nil, errors.New("tools: guard policy is nil")
	}
	undo := owner.store.addGuard(ownerScope.Target(), policy)
	return owner.ownMutation(context.Background(), ownerScope, "tools.guard()", undo, false)
}

func (owner *toolRegistry) Get(name string, selectedKey plugin.ScopeKey) (ToolDefinition, bool) {
	entry, found := owner.store.view(selectedKey).visible[name]
	if !found {
		return ToolDefinition{}, false
	}
	return cloneDefinition(entry.definition), true
}

func (owner *toolRegistry) Schemas(selectedKey plugin.ScopeKey) []llm.ToolSchema {
	return owner.promptTools(selectedKey).Schemas
}

func (owner *toolRegistry) ExecutionMode(input ToolExecutionInput) ToolExecutionMode {
	arguments, err := jsonvalue.Clone(input.Arguments)
	if err != nil {
		return ExecutionExclusive
	}
	entry, found := owner.store.view(input.Scope).visible[input.Name]
	if !found || entry.definition.ConcurrencyBehavior == nil {
		return ExecutionExclusive
	}
	if err := validateSchemaValue(entry.parameterSchema, arguments, "arguments"); err != nil {
		return ExecutionExclusive
	}
	if concurrencySafe(entry.definition.ConcurrencyBehavior, arguments) {
		return ExecutionParallel
	}
	return ExecutionExclusive
}

func (owner *toolRegistry) promptTools(selectedKey plugin.ScopeKey) systemprompt.ToolProviderResult {
	resolved := owner.store.view(selectedKey)
	projections := make([]llm.ToolSchema, 0, len(resolved.order))
	for _, name := range resolved.order {
		entry := resolved.visible[name]
		if entry == nil {
			continue
		}
		projections = append(projections, llm.ToolSchema{
			Name: entry.definition.Name, Description: entry.definition.Description,
			Parameters: append(json.RawMessage(nil), entry.definition.Parameters...),
		})
	}
	return systemprompt.ToolProviderResult{Schemas: projections, KnownNames: sortedNames(resolved.knownNames)}
}

func (owner *toolRegistry) ownMutation(
	requestContext context.Context,
	ownerScope *plugin.Scope,
	label string,
	undo func(),
	notifyChange bool,
) (plugin.Disposer, error) {
	var initializing atomic.Bool
	initializing.Store(true)
	release, err := plugin.Own(ownerScope, label, func(closeContext context.Context) error {
		undo()
		if initializing.Load() || !notifyChange {
			return nil
		}
		return plugin.EmitFrom(closeContext, owner.sourceScope, changeEvent, struct{}{})
	})
	if err != nil {
		undo()
		return nil, err
	}
	if notifyChange {
		if err := plugin.EmitFrom(requestContext, owner.sourceScope, changeEvent, struct{}{}); err != nil {
			return nil, errors.Join(err, release(requestContext))
		}
	}
	initializing.Store(false)
	return release, nil
}
