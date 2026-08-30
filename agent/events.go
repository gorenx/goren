package agent

import (
	"context"

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

func (Created) AgentScopedEvent() {}
func (Created) EventName() string { return CreatedEventName }
func (Created) EventDelivery() plugin.DeliveryPolicy {
	return plugin.DeliveryOrdered
}

// Disposed announces exact live Agent removal.
type Disposed struct {
	Subject Agent
}

func (Disposed) AgentScopedEvent() {}
func (Disposed) EventName() string { return DisposedEventName }
func (Disposed) EventDelivery() plugin.DeliveryPolicy {
	return plugin.DeliveryOrdered
}

// StatusChanged carries one non-repeating destination state.
type StatusChanged struct {
	Subject Agent
	Status  Status
}

func (StatusChanged) AgentScopedEvent() {}
func (StatusChanged) EventName() string { return StatusEventName }
func (StatusChanged) EventDelivery() plugin.DeliveryPolicy {
	return plugin.DeliveryOrdered
}

// InboxInserted carries one committed live Inbox insertion.
type InboxInserted struct {
	Subject Agent
	Message agentmessage.UserMessage
}

func (InboxInserted) AgentScopedEvent() {}
func (InboxInserted) EventName() string { return InboxInsertedEventName }
func (InboxInserted) EventDelivery() plugin.DeliveryPolicy {
	return plugin.DeliveryOrdered
}

// InboxClaimed carries one committed Inbox claim.
type InboxClaimed struct {
	Subject Agent
	Message agentmessage.UserMessage
	Turn    int64
}

func (InboxClaimed) AgentScopedEvent() {}
func (InboxClaimed) EventName() string { return InboxClaimedEventName }
func (InboxClaimed) EventDelivery() plugin.DeliveryPolicy {
	return plugin.DeliveryOrdered
}

// InboxDiscarded carries one committed Inbox removal without execution.
type InboxDiscarded struct {
	Subject Agent
	Message agentmessage.UserMessage
}

func (InboxDiscarded) AgentScopedEvent() {}
func (InboxDiscarded) EventName() string { return InboxDiscardedEventName }
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

func (SessionStarted) AgentScopedEvent() {}
func (SessionStarted) EventName() string { return SessionStartEventName }
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

func (TurnStopping) AgentScopedEvent() {}
func (TurnStopping) EventName() string { return TurnStoppingEventName }
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

func (AgentError) AgentScopedEvent() {}
func (AgentError) EventName() string { return ErrorEventName }
func (AgentError) EventDelivery() plugin.DeliveryPolicy {
	return plugin.DeliveryOrdered
}

// AgentEvent is an event intentionally published from one exact Agent Scope.
// Producer modules own their event types and opt in through the marker.
type AgentEvent interface {
	AgentScopedEvent()
}

// AgentClosingEvent is a terminal fact allowed through an exact Agent Scope
// while Registry closes descendants. It cannot admit work or mutate Scope.
type AgentClosingEvent interface {
	AgentEvent
	AgentClosingEvent()
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
