package agent

import (
	"context"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/llm"
)

// orderedEventDelivery preserves source-compatible sequential Agent event
// observation without importing Plugin Runtime types.
const orderedEventDelivery = "ordered"

const (
	// CreatedEventName identifies exact Agent publication.
	CreatedEventName = "agent/created"
	// DisposedEventName identifies exact Agent retirement.
	DisposedEventName = "agent/disposed"
	// StatusEventName identifies an Agent status transition.
	StatusEventName = "agent/status"
	// InboxInsertedEventName identifies a committed Inbox insertion.
	InboxInsertedEventName = "agent/inbox/inserted"
	// InboxClaimedEventName identifies a committed Inbox claim.
	InboxClaimedEventName = "agent/inbox/claimed"
	// InboxDiscardedEventName identifies an Inbox removal without execution.
	InboxDiscardedEventName = "agent/inbox/discarded"
	// SessionStartEventName identifies the first Session driving edge.
	SessionStartEventName = "agent/session-start"
	// PreStepEventName identifies pre-step decision interception.
	PreStepEventName = "agent/pre-step"
	// RequestEventName identifies model request resolution.
	RequestEventName = "agent/request"
	// RequestErrorEventName identifies failed request recovery.
	RequestErrorEventName = "agent/request-error"
	// TurnStoppingEventName identifies the ordered turn stop boundary.
	TurnStoppingEventName = "agent/turn-stopping"
	// ErrorEventName identifies a contained Agent execution failure.
	ErrorEventName = "agent/error"
)

// Created is the vetoable live Agent publication edge.
type Created struct {
	Subject Agent
}

func (Created) AgentScopedEvent() {}
func (Created) EventName() string { return CreatedEventName }
func (Created) EventDelivery() string {
	return orderedEventDelivery
}

// Disposed announces exact live Agent removal.
type Disposed struct {
	Subject Agent
}

func (Disposed) AgentScopedEvent() {}
func (Disposed) EventName() string { return DisposedEventName }
func (Disposed) EventDelivery() string {
	return orderedEventDelivery
}

// StatusChanged carries one non-repeating destination state.
type StatusChanged struct {
	Subject Agent
	Status  Status
}

func (StatusChanged) AgentScopedEvent() {}
func (StatusChanged) EventName() string { return StatusEventName }
func (StatusChanged) EventDelivery() string {
	return orderedEventDelivery
}

// InboxInserted carries one committed live Inbox insertion.
type InboxInserted struct {
	Subject Agent
	Message agentmessage.UserMessage
}

func (InboxInserted) AgentScopedEvent() {}
func (InboxInserted) EventName() string { return InboxInsertedEventName }
func (InboxInserted) EventDelivery() string {
	return orderedEventDelivery
}

// InboxClaimed carries one committed Inbox claim.
type InboxClaimed struct {
	Subject Agent
	Message agentmessage.UserMessage
	Turn    int64
}

func (InboxClaimed) AgentScopedEvent() {}
func (InboxClaimed) EventName() string { return InboxClaimedEventName }
func (InboxClaimed) EventDelivery() string {
	return orderedEventDelivery
}

// InboxDiscarded carries one committed Inbox removal without execution.
type InboxDiscarded struct {
	Subject Agent
	Message agentmessage.UserMessage
}

func (InboxDiscarded) AgentScopedEvent() {}
func (InboxDiscarded) EventName() string { return InboxDiscardedEventName }
func (InboxDiscarded) EventDelivery() string {
	return orderedEventDelivery
}

// SessionStartSource classifies why an Agent's Session lifecycle began.
type SessionStartSource string

const (
	// SessionStartup means a new Session was created.
	SessionStartup SessionStartSource = "startup"
	// SessionResume means durable Session state was restored.
	SessionResume SessionStartSource = "resume"
	// SessionClear means the active Session was cleared.
	SessionClear SessionStartSource = "clear"
	// SessionCompact means compaction began a replacement Session view.
	SessionCompact SessionStartSource = "compact"
)

// SessionStarted announces the first driving extension point.
type SessionStarted struct {
	Subject Agent
	Source  SessionStartSource
}

func (SessionStarted) AgentScopedEvent() {}
func (SessionStarted) EventName() string { return SessionStartEventName }
func (SessionStarted) EventDelivery() string {
	return orderedEventDelivery
}

// PreStepKind selects rejection or entry for a proposed model step.
type PreStepKind string

const (
	// PreStepReject prevents the proposed model step.
	PreStepReject PreStepKind = "reject"
	// PreStepEnter admits the proposed model step.
	PreStepEnter PreStepKind = "enter"
)

// PreStepNotice is the typed input of the Agent pre-step Waterfall.
type PreStepNotice struct {
	Subject  Agent
	Messages []agentmessage.UserMessage
	Turn     int64
	Step     int64
}

func (PreStepNotice) RuntimeWaterfallInput() {}

// PreStepDecision decides whether and with which messages a step starts.
type PreStepDecision struct {
	Kind     PreStepKind
	Messages []agentmessage.UserMessage
}

func (PreStepDecision) RuntimeWaterfallOutput() {}

// RequestNotice identifies the step whose immutable call config is resolving.
type RequestNotice struct {
	Subject Agent
	Turn    int64
	Step    int64
}

func (RequestNotice) RuntimeWaterfallInput() {}

// RequestResolution is the typed output of the Agent request Waterfall.
type RequestResolution struct {
	Config llm.CallConfig
}

func (RequestResolution) RuntimeWaterfallOutput() {}

// RequestErrorNotice contains provider-neutral failed-attempt policy facts.
type RequestErrorNotice struct {
	Subject     Agent
	Turn        int64
	Step        int64
	Provider    string
	Failure     llm.LlmFailure
	RetryPolicy llm.RetryPolicy
}

func (RequestErrorNotice) RuntimeWaterfallInput() {}

// RequestErrorAction lets one Middleware own recovery for a failed attempt.
type RequestErrorAction struct {
	Retry bool
}

func (RequestErrorAction) RuntimeWaterfallOutput() {}

// TurnStopping is the ordered turn-boundary Event.
type TurnStopping struct {
	Subject Agent
	Turn    int64
}

func (TurnStopping) AgentScopedEvent() {}
func (TurnStopping) EventName() string { return TurnStoppingEventName }
func (TurnStopping) EventDelivery() string {
	return orderedEventDelivery
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
func (AgentError) EventDelivery() string {
	return orderedEventDelivery
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
