package agent

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
)

const (
	CreatedEventName        = "agent/created"
	DisposedEventName       = "agent/disposed"
	StatusEventName         = "agent/status"
	InboxInsertedEventName  = "agent/inbox/inserted"
	InboxClaimedEventName   = "agent/inbox/claimed"
	InboxDiscardedEventName = "agent/inbox/discarded"
	SessionStartEventName   = "agent/session-start"
	PreStepEventName        = "agent/pre-step"
	RequestEventName        = "agent/request"
	RequestErrorEventName   = "agent/request-error"
	TurnStoppingEventName   = "agent/turn-stopping"
	ErrorEventName          = "agent/error"
)

// Created is the vetoable live Agent publication edge.
type Created struct {
	Subject Agent
}

func (Created) AgentScopedRuntimeEvent() {}
func (Created) EventName() string        { return CreatedEventName }
func (Created) EventDelivery() plugin.DeliveryPolicy {
	return plugin.DeliveryOrdered
}

// Disposed announces exact live Agent removal.
type Disposed struct {
	Subject Agent
}

func (Disposed) AgentScopedRuntimeEvent() {}
func (Disposed) EventName() string        { return DisposedEventName }
func (Disposed) EventDelivery() plugin.DeliveryPolicy {
	return plugin.DeliveryOrdered
}

// StatusChanged carries one non-repeating destination state.
type StatusChanged struct {
	Subject Agent
	Status  Status
}

func (StatusChanged) AgentScopedRuntimeEvent() {}
func (StatusChanged) EventName() string        { return StatusEventName }
func (StatusChanged) EventDelivery() plugin.DeliveryPolicy {
	return plugin.DeliveryOrdered
}

// InboxInserted carries one committed live Inbox insertion.
type InboxInserted struct {
	Subject Agent
	Message agentmessage.UserMessage
}

func (InboxInserted) AgentScopedRuntimeEvent() {}
func (InboxInserted) EventName() string        { return InboxInsertedEventName }
func (InboxInserted) EventDelivery() plugin.DeliveryPolicy {
	return plugin.DeliveryOrdered
}

// InboxClaimed carries one committed Inbox claim.
type InboxClaimed struct {
	Subject Agent
	Message agentmessage.UserMessage
	Turn    int64
}

func (InboxClaimed) AgentScopedRuntimeEvent() {}
func (InboxClaimed) EventName() string        { return InboxClaimedEventName }
func (InboxClaimed) EventDelivery() plugin.DeliveryPolicy {
	return plugin.DeliveryOrdered
}

// InboxDiscarded carries one committed Inbox removal without execution.
type InboxDiscarded struct {
	Subject Agent
	Message agentmessage.UserMessage
}

func (InboxDiscarded) AgentScopedRuntimeEvent() {}
func (InboxDiscarded) EventName() string        { return InboxDiscardedEventName }
func (InboxDiscarded) EventDelivery() plugin.DeliveryPolicy {
	return plugin.DeliveryOrdered
}

// SessionStartSource classifies why an Agent's Session lifecycle began.
type SessionStartSource string

const (
	SessionStartup SessionStartSource = "startup"
	SessionResume  SessionStartSource = "resume"
	SessionClear   SessionStartSource = "clear"
	SessionCompact SessionStartSource = "compact"
)

// SessionStarted announces the first driving extension point.
type SessionStarted struct {
	Subject Agent
	Source  SessionStartSource
}

func (SessionStarted) AgentScopedRuntimeEvent() {}
func (SessionStarted) EventName() string        { return SessionStartEventName }
func (SessionStarted) EventDelivery() plugin.DeliveryPolicy {
	return plugin.DeliveryOrdered
}

// PreStepKind selects rejection or entry for a proposed model step.
type PreStepKind string

const (
	PreStepReject PreStepKind = "reject"
	PreStepEnter  PreStepKind = "enter"
)

// PreStepNotice is the typed input of the Agent pre-step Waterfall.
type PreStepNotice struct {
	plugin.WaterfallInputBase
	Subject  Agent
	Messages []agentmessage.UserMessage
	Turn     int64
	Step     int64
}

// PreStepDecision decides whether and with which messages a step starts.
type PreStepDecision struct {
	plugin.WaterfallOutputBase
	Kind     PreStepKind
	Messages []agentmessage.UserMessage
}

// RequestNotice identifies the step whose immutable call config is resolving.
type RequestNotice struct {
	plugin.WaterfallInputBase
	Subject Agent
	Turn    int64
	Step    int64
}

// RequestResolution is the typed output of the Agent request Waterfall.
type RequestResolution struct {
	plugin.WaterfallOutputBase
	Config llm.CallConfig
}

// RequestErrorNotice contains provider-neutral failed-attempt policy facts.
type RequestErrorNotice struct {
	plugin.WaterfallInputBase
	Subject     Agent
	Turn        int64
	Step        int64
	Provider    string
	Failure     llm.LlmFailure
	RetryPolicy llm.RetryPolicy
}

// RequestErrorAction lets one Middleware own recovery for a failed attempt.
type RequestErrorAction struct {
	plugin.WaterfallOutputBase
	Retry bool
}

// TurnStopping is the ordered turn-boundary Event.
type TurnStopping struct {
	Subject Agent
	Turn    int64
}

func (TurnStopping) AgentScopedRuntimeEvent() {}
func (TurnStopping) EventName() string        { return TurnStoppingEventName }
func (TurnStopping) EventDelivery() plugin.DeliveryPolicy {
	return plugin.DeliveryOrdered
}

// AgentError is a contained live failure notification.
type AgentError struct {
	Subject Agent
	Turn    int64
	Step    int64
	Err     error
}

func (AgentError) AgentScopedRuntimeEvent() {}
func (AgentError) EventName() string        { return ErrorEventName }
func (AgentError) EventDelivery() plugin.DeliveryPolicy {
	return plugin.DeliveryOrdered
}

// RuntimeEvent is an event intentionally published from one exact Agent Scope.
// Producer modules own their event types and opt in through the marker.
type RuntimeEvent interface {
	AgentScopedRuntimeEvent()
}

type scopeRuntimeCarrier interface {
	ScopeRuntimeValue() AgentScopeRuntime
}

func scopeRuntimeOf(subject Agent) AgentScopeRuntime {
	carrier, matches := subject.(scopeRuntimeCarrier)
	if !matches {
		return nil
	}
	return carrier.ScopeRuntimeValue()
}

// DispatchRuntimeEvent publishes one producer-owned fact from the exact Agent
// Scope without exposing the Scope adapter on the Agent capability.
func DispatchRuntimeEvent(
	requestContext context.Context,
	subject Agent,
	fact RuntimeEvent,
) error {
	if subject == nil || fact == nil {
		return errors.New("agent: RuntimeEvent subject or fact is nil")
	}
	runtime := scopeRuntimeOf(subject)
	if runtime == nil {
		return errors.New("agent: RuntimeEvent effects are unavailable")
	}
	return runtime.Dispatch(requestContext, fact)
}

// PreStepAction is the Agent-owned terminal contract for pre-step resolution.
type PreStepAction interface {
	Execute(context.Context, PreStepNotice) (PreStepDecision, error)
}

// RequestAction is the Agent-owned terminal contract for request resolution.
type RequestAction interface {
	Execute(context.Context, RequestNotice) (RequestResolution, error)
}

// RequestErrorHandler is the Agent-owned terminal contract for failed request
// recovery.
type RequestErrorHandler interface {
	Execute(context.Context, RequestErrorNotice) (RequestErrorAction, error)
}

// ResolvePreStep runs the scoped pre-step Waterfall around terminal.
func ResolvePreStep(
	requestContext context.Context,
	notice PreStepNotice,
	terminal PreStepAction,
) (PreStepDecision, error) {
	if notice.Subject == nil || terminal == nil {
		return PreStepDecision{}, errors.New(
			"agent: pre-step subject or terminal is nil",
		)
	}
	runtime := scopeRuntimeOf(notice.Subject)
	if runtime == nil {
		return PreStepDecision{}, errors.New("agent: pre-step effects are unavailable")
	}
	return runtime.ResolvePreStep(
		requestContext,
		notice,
		terminal,
	)
}

// ResolveRequest runs the scoped request Waterfall around terminal.
func ResolveRequest(
	requestContext context.Context,
	notice RequestNotice,
	terminal RequestAction,
) (llm.CallConfig, error) {
	if notice.Subject == nil || terminal == nil {
		return llm.CallConfig{}, errors.New(
			"agent: request subject or terminal is nil",
		)
	}
	runtime := scopeRuntimeOf(notice.Subject)
	if runtime == nil {
		return llm.CallConfig{}, errors.New("agent: request effects are unavailable")
	}
	resolved, err := runtime.ResolveRequest(
		requestContext,
		notice,
		terminal,
	)
	return resolved.Config, err
}

// ResolveRequestError runs the scoped recovery Waterfall around terminal.
func ResolveRequestError(
	requestContext context.Context,
	notice RequestErrorNotice,
	terminal RequestErrorHandler,
) (RequestErrorAction, error) {
	if notice.Subject == nil || terminal == nil {
		return RequestErrorAction{}, errors.New(
			"agent: request-error subject or terminal is nil",
		)
	}
	runtime := scopeRuntimeOf(notice.Subject)
	if runtime == nil {
		return RequestErrorAction{}, errors.New(
			"agent: request-error effects are unavailable",
		)
	}
	return runtime.ResolveRequestError(
		requestContext,
		notice,
		terminal,
	)
}
