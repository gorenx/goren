package agent

import (
	"context"

	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/tools"
)

// ScopeResource is one exact idempotent resource owned by an Agent Scope.
// Close may be called before Scope shutdown; later shutdown must remain safe.
type ScopeResource interface {
	Close(context.Context) error
}

// ScopeResources is the committed resource set produced by one Setup. It has
// no Commit operation because publication validation belongs to the private
// Scope draft that created it.
type ScopeResources interface {
	Close(context.Context) error
}

// Scope is the lifecycle and extension boundary of one exact Agent. Registry
// owns it; RLA receives narrower event, waterfall, Tool, and Prompt ports.
type Scope interface {
	ApplySetup(context.Context, Agent, Setup) (ScopeResources, error)
	Dispatch(context.Context, AgentEvent) error
	ResolvePreStep(
		context.Context,
		PreStepNotice,
		PreStepAction,
	) (PreStepDecision, error)
	ResolveRequest(
		context.Context,
		RequestNotice,
		RequestAction,
	) (RequestResolution, error)
	ResolveRequestError(
		context.Context,
		RequestErrorNotice,
		RequestErrorHandler,
	) (RequestErrorAction, error)
	Close(context.Context) error
}

// ScopeCheck validates facts that may change while a Setup is applied. Check
// runs at the private Scope draft commit boundary and must not acquire a new
// resource or perform an irreversible external effect.
type ScopeCheck interface {
	Check() error
}

// ScopeEditor is the Setup-owned write boundary for one private Scope draft.
// Every successful Add or Observe call is included in the resulting
// ScopeResources automatically.
type ScopeEditor interface {
	ApplyNestedSetup(context.Context, Setup) (ScopeResources, error)
	AddPromptSection(context.Context, systemprompt.PromptSection) error
	AddPromptVariable(context.Context, string, systemprompt.VariableProvider) error
	UsePromptAssembly(systemprompt.AssemblyMiddleware) error
	SuppressRuntimeContext(context.Context, string) error
	AddTool(context.Context, tools.ToolDefinition) error
	AddToolRestriction(context.Context, string, tools.ToolRestriction) error
	AddToolGuard(context.Context, string, tools.ToolGuard) error
	UseToolExecution(tools.ExecuteMiddleware) error
	ObserveAgentEvents(AgentEventObserver) error
	UsePreStep(PreStepMiddleware) error
	UseRequest(RequestMiddleware) error
	UseRequestError(RequestErrorMiddleware) error
	ObserveToolResults(tools.ResultObserver) error
	Own(ScopeResource) error
	Check(ScopeCheck) error
}

// AgentEventObserver observes events emitted by one exact Agent Scope.
type AgentEventObserver interface {
	ObserveAgentEvent(context.Context, AgentEvent) error
}

// PreStepMiddleware wraps one Agent pre-step decision.
type PreStepMiddleware interface {
	InterceptPreStep(
		context.Context,
		PreStepNotice,
		PreStepAction,
	) (PreStepDecision, error)
}

// RequestMiddleware wraps one Agent model-request resolution.
type RequestMiddleware interface {
	InterceptRequest(
		context.Context,
		RequestNotice,
		RequestAction,
	) (RequestResolution, error)
}

// RequestErrorMiddleware wraps one failed model-request decision.
type RequestErrorMiddleware interface {
	InterceptRequestError(
		context.Context,
		RequestErrorNotice,
		RequestErrorHandler,
	) (RequestErrorAction, error)
}
