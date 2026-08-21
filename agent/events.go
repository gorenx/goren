package agent

import (
	"context"
	"errors"

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

func (Created) EventName() string { return CreatedEventName }
func (Created) EventDelivery() plugin.DeliveryPolicy {
	return plugin.DeliveryOrdered
}

// Disposed announces exact live Agent removal.
type Disposed struct {
	Subject Agent
}

func (Disposed) EventName() string { return DisposedEventName }
func (Disposed) EventDelivery() plugin.DeliveryPolicy {
	return plugin.DeliveryOrdered
}

// StatusChanged carries one non-repeating destination state.
type StatusChanged struct {
	Subject Agent
	Status  Status
}

func (StatusChanged) EventName() string { return StatusEventName }
func (StatusChanged) EventDelivery() plugin.DeliveryPolicy {
	return plugin.DeliveryOrdered
}

// InboxInserted carries one committed live Inbox insertion.
type InboxInserted struct {
	Subject Agent
	Message llm.UserMessage
}

func (InboxInserted) EventName() string { return InboxInsertedEventName }
func (InboxInserted) EventDelivery() plugin.DeliveryPolicy {
	return plugin.DeliveryOrdered
}

// InboxClaimed carries one committed Inbox claim.
type InboxClaimed struct {
	Subject Agent
	Message llm.UserMessage
	Turn    int64
}

func (InboxClaimed) EventName() string { return InboxClaimedEventName }
func (InboxClaimed) EventDelivery() plugin.DeliveryPolicy {
	return plugin.DeliveryOrdered
}

// InboxDiscarded carries one committed Inbox removal without execution.
type InboxDiscarded struct {
	Subject Agent
	Message llm.UserMessage
}

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
	Messages []llm.UserMessage
	Turn     int64
	Step     int64
}

// PreStepDecision decides whether and with which messages a step starts.
type PreStepDecision struct {
	plugin.WaterfallOutputBase
	Kind     PreStepKind
	Messages []llm.UserMessage
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

func (AgentError) EventName() string { return ErrorEventName }
func (AgentError) EventDelivery() plugin.DeliveryPolicy {
	return plugin.DeliveryOrdered
}

// PreStepActionFunc adapts a stateless terminal operation.
type PreStepActionFunc func(context.Context, PreStepNotice) (PreStepDecision, error)

func (operation PreStepActionFunc) Execute(
	requestContext context.Context,
	notice PreStepNotice,
) (PreStepDecision, error) {
	return operation(requestContext, notice)
}

// RequestActionFunc adapts a stateless request terminal operation.
type RequestActionFunc func(context.Context, RequestNotice) (RequestResolution, error)

func (operation RequestActionFunc) Execute(
	requestContext context.Context,
	notice RequestNotice,
) (RequestResolution, error) {
	return operation(requestContext, notice)
}

// RequestErrorActionFunc adapts a stateless recovery terminal operation.
type RequestErrorActionFunc func(context.Context, RequestErrorNotice) (RequestErrorAction, error)

func (operation RequestErrorActionFunc) Execute(
	requestContext context.Context,
	notice RequestErrorNotice,
) (RequestErrorAction, error) {
	return operation(requestContext, notice)
}

// ResolvePreStep runs the scoped pre-step Waterfall around terminal.
func ResolvePreStep(
	requestContext context.Context,
	notice PreStepNotice,
	terminal plugin.WaterfallAction[PreStepNotice, PreStepDecision],
) (PreStepDecision, error) {
	if notice.Subject == nil || terminal == nil {
		return PreStepDecision{}, errors.New(
			"agent: pre-step subject or terminal is nil",
		)
	}
	return plugin.Run(
		requestContext,
		notice.Subject,
		notice,
		terminal,
	)
}

// ResolveRequest runs the scoped request Waterfall around terminal.
func ResolveRequest(
	requestContext context.Context,
	notice RequestNotice,
	terminal plugin.WaterfallAction[RequestNotice, RequestResolution],
) (llm.CallConfig, error) {
	if notice.Subject == nil || terminal == nil {
		return llm.CallConfig{}, errors.New(
			"agent: request subject or terminal is nil",
		)
	}
	resolved, err := plugin.Run(
		requestContext,
		notice.Subject,
		notice,
		terminal,
	)
	return resolved.Config, err
}

// ResolveRequestError runs the scoped recovery Waterfall around terminal.
func ResolveRequestError(
	requestContext context.Context,
	notice RequestErrorNotice,
	terminal plugin.WaterfallAction[RequestErrorNotice, RequestErrorAction],
) (RequestErrorAction, error) {
	if notice.Subject == nil || terminal == nil {
		return RequestErrorAction{}, errors.New(
			"agent: request-error subject or terminal is nil",
		)
	}
	return plugin.Run(
		requestContext,
		notice.Subject,
		notice,
		terminal,
	)
}
