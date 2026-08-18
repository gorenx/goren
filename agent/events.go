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

// LifecycleNotice carries the exact Agent subject of a creation or disposal.
type LifecycleNotice struct {
	Subject Agent
}

// StatusNotice carries one non-repeating destination state.
type StatusNotice struct {
	Subject Agent
	Status  Status
}

// InboxNotice carries one committed live inbox change.
type InboxNotice struct {
	Subject Agent
	Message llm.UserMessage
	Turn    int64
}

// SessionStartSource classifies why an Agent's Session lifecycle began.
type SessionStartSource string

const (
	SessionStartup SessionStartSource = "startup"
	SessionResume  SessionStartSource = "resume"
	SessionClear   SessionStartSource = "clear"
	SessionCompact SessionStartSource = "compact"
)

// SessionStartNotice announces the first driving extension point.
type SessionStartNotice struct {
	Subject Agent
	Source  SessionStartSource
}

// PreStepKind selects rejection or entry for a proposed model step.
type PreStepKind string

const (
	PreStepReject PreStepKind = "reject"
	PreStepEnter  PreStepKind = "enter"
)

// PreStepDecision decides whether and with which messages a step starts.
type PreStepDecision struct {
	Kind     PreStepKind
	Messages []llm.UserMessage
}

// PreStepNotice describes one proposed step. Cancellation is carried by the
// dispatch context instead of retained in the payload.
type PreStepNotice struct {
	Subject  Agent
	Messages []llm.UserMessage
	Turn     int64
	Step     int64
}

// RequestNotice identifies the step whose immutable call config is resolving.
type RequestNotice struct {
	Subject Agent
	Turn    int64
	Step    int64
}

// RequestErrorAction lets one listener own recovery for a failed attempt.
type RequestErrorAction struct {
	Retry bool
}

// RequestErrorNotice contains provider-neutral facts for failed-attempt policy.
type RequestErrorNotice struct {
	Subject     Agent
	Turn        int64
	Step        int64
	Provider    string
	Failure     llm.LlmFailure
	RetryPolicy llm.RetryPolicy
}

// TurnNotice identifies a turn boundary.
type TurnNotice struct {
	Subject Agent
	Turn    int64
}

// ErrorNotice is the contained live failure notification.
type ErrorNotice struct {
	Subject Agent
	Turn    int64
	Step    int64
	Err     error
}

var (
	createdEvent        = plugin.DefineEvent[LifecycleNotice, struct{}](CreatedEventName, plugin.ModeEmit)
	disposedEvent       = plugin.DefineEvent[LifecycleNotice, struct{}](DisposedEventName, plugin.ModeEmit)
	statusEvent         = plugin.DefineEvent[StatusNotice, struct{}](StatusEventName, plugin.ModeEmit)
	inboxInsertedEvent  = plugin.DefineEvent[InboxNotice, struct{}](InboxInsertedEventName, plugin.ModeEmit)
	inboxClaimedEvent   = plugin.DefineEvent[InboxNotice, struct{}](InboxClaimedEventName, plugin.ModeEmit)
	inboxDiscardedEvent = plugin.DefineEvent[InboxNotice, struct{}](InboxDiscardedEventName, plugin.ModeEmit)
	sessionStartEvent   = plugin.DefineEvent[SessionStartNotice, struct{}](SessionStartEventName, plugin.ModeEmit)
	preStepEvent        = plugin.DefineEvent[PreStepNotice, PreStepDecision](PreStepEventName, plugin.ModeWaterfall)
	requestEvent        = plugin.DefineEvent[RequestNotice, llm.CallConfig](RequestEventName, plugin.ModeWaterfall)
	requestErrorEvent   = plugin.DefineEvent[RequestErrorNotice, RequestErrorAction](RequestErrorEventName, plugin.ModeWaterfall)
	turnStoppingEvent   = plugin.DefineEvent[TurnNotice, struct{}](TurnStoppingEventName, plugin.ModeSerial)
	errorEvent          = plugin.DefineEvent[ErrorNotice, struct{}](ErrorEventName, plugin.ModeEmit)
)

type LifecycleHandler func(context.Context, Agent) error
type StatusHandler func(context.Context, Agent, Status) error
type InboxHandler func(context.Context, Agent, llm.UserMessage, int64) error
type SessionStartHandler func(context.Context, Agent, SessionStartSource) error
type PreStepNext func(context.Context) (PreStepDecision, error)
type PreStepHandler func(context.Context, PreStepNotice, PreStepNext) (PreStepDecision, error)
type RequestNext func(context.Context) (llm.CallConfig, error)
type RequestHandler func(context.Context, RequestNotice, RequestNext) (llm.CallConfig, error)
type RequestErrorNext func(context.Context) (RequestErrorAction, error)
type RequestErrorHandler func(context.Context, RequestErrorNotice, RequestErrorNext) (RequestErrorAction, error)
type TurnStoppingHandler func(context.Context, Agent, int64) error
type ErrorHandler func(context.Context, ErrorNotice) error

func OnCreated(pluginScope *plugin.Scope, callback LifecycleHandler) (plugin.Disposer, error) {
	return onLifecycle(pluginScope, createdEvent, callback)
}

func OnDisposed(pluginScope *plugin.Scope, callback LifecycleHandler) (plugin.Disposer, error) {
	return onLifecycle(pluginScope, disposedEvent, callback)
}

func onLifecycle(pluginScope *plugin.Scope, topic plugin.EventKey[LifecycleNotice, struct{}], callback LifecycleHandler) (plugin.Disposer, error) {
	if callback == nil {
		return nil, errors.New("agent: lifecycle handler is nil")
	}
	return plugin.OnNotify(pluginScope, topic, func(requestContext context.Context, notice LifecycleNotice) error {
		return callback(requestContext, notice.Subject)
	})
}

func OnStatus(pluginScope *plugin.Scope, callback StatusHandler) (plugin.Disposer, error) {
	if callback == nil {
		return nil, errors.New("agent: status handler is nil")
	}
	return plugin.OnNotify(pluginScope, statusEvent, func(requestContext context.Context, notice StatusNotice) error {
		return callback(requestContext, notice.Subject, notice.Status)
	})
}

func OnInboxInserted(pluginScope *plugin.Scope, callback InboxHandler) (plugin.Disposer, error) {
	return onInbox(pluginScope, inboxInsertedEvent, callback)
}

func OnInboxClaimed(pluginScope *plugin.Scope, callback InboxHandler) (plugin.Disposer, error) {
	return onInbox(pluginScope, inboxClaimedEvent, callback)
}

func OnInboxDiscarded(pluginScope *plugin.Scope, callback InboxHandler) (plugin.Disposer, error) {
	return onInbox(pluginScope, inboxDiscardedEvent, callback)
}

func onInbox(pluginScope *plugin.Scope, topic plugin.EventKey[InboxNotice, struct{}], callback InboxHandler) (plugin.Disposer, error) {
	if callback == nil {
		return nil, errors.New("agent: inbox handler is nil")
	}
	return plugin.OnNotify(
		pluginScope,
		topic,
		func(requestContext context.Context, notice InboxNotice) error {
			return callback(requestContext, notice.Subject, notice.Message, notice.Turn)
		},
	)
}

func OnSessionStart(pluginScope *plugin.Scope, callback SessionStartHandler) (plugin.Disposer, error) {
	if callback == nil {
		return nil, errors.New("agent: session-start handler is nil")
	}
	return plugin.OnNotify(pluginScope, sessionStartEvent, func(requestContext context.Context, notice SessionStartNotice) error {
		return callback(requestContext, notice.Subject, notice.Source)
	})
}

func OnPreStep(pluginScope *plugin.Scope, callback PreStepHandler) (plugin.Disposer, error) {
	if callback == nil {
		return nil, errors.New("agent: pre-step handler is nil")
	}
	return plugin.OnWaterfall(
		pluginScope,
		preStepEvent,
		func(
			requestContext context.Context,
			notice PreStepNotice,
			downstream plugin.Next[PreStepNotice, PreStepDecision],
		) (PreStepDecision, error) {
			return callback(
				requestContext, notice,
				func(chainContext context.Context) (PreStepDecision, error) {
					return downstream(chainContext, notice)
				},
			)
		},
	)
}

func OnRequest(pluginScope *plugin.Scope, callback RequestHandler) (plugin.Disposer, error) {
	if callback == nil {
		return nil, errors.New("agent: request handler is nil")
	}
	return plugin.OnWaterfall(
		pluginScope,
		requestEvent,
		func(
			requestContext context.Context,
			notice RequestNotice, downstream plugin.Next[RequestNotice, llm.CallConfig],
		) (llm.CallConfig, error) {
			return callback(
				requestContext,
				notice,
				func(chainContext context.Context) (llm.CallConfig, error) {
					return downstream(chainContext, notice)
				},
			)
		},
	)
}

func OnRequestError(pluginScope *plugin.Scope, callback RequestErrorHandler) (plugin.Disposer, error) {
	if callback == nil {
		return nil, errors.New("agent: request-error handler is nil")
	}
	return plugin.OnWaterfall(pluginScope, requestErrorEvent,
		func(
			requestContext context.Context,
			notice RequestErrorNotice,
			downstream plugin.Next[RequestErrorNotice, RequestErrorAction],
		) (RequestErrorAction, error) {
			return callback(
				requestContext,
				notice,
				func(chainContext context.Context) (RequestErrorAction, error) {
					return downstream(chainContext, notice)
				},
			)
		},
	)
}

func OnTurnStopping(pluginScope *plugin.Scope, callback TurnStoppingHandler) (plugin.Disposer, error) {
	if callback == nil {
		return nil, errors.New("agent: turn-stopping handler is nil")
	}
	return plugin.OnDecision(pluginScope, turnStoppingEvent,
		func(requestContext context.Context, notice TurnNotice) (plugin.Decision[struct{}], error) {
			return plugin.Decision[struct{}]{}, callback(requestContext, notice.Subject, notice.Turn)
		})
}

func OnError(pluginScope *plugin.Scope, callback ErrorHandler) (plugin.Disposer, error) {
	if callback == nil {
		return nil, errors.New("agent: error handler is nil")
	}
	return plugin.OnNotify(pluginScope, errorEvent,
		func(requestContext context.Context, notice ErrorNotice) error {
			return callback(requestContext, notice)
		},
	)
}

func emitScoped[P any](requestContext context.Context, sourceScope *plugin.Scope, subject Agent, topic plugin.EventKey[P, struct{}], payload P) error {
	if subject == nil || subject.ScopeValue() == nil {
		return errors.New("agent: scoped event subject is nil")
	}
	return plugin.EmitScopedFrom(requestContext, sourceScope, subject.ScopeValue().Target(), topic, payload)
}

func EmitStatus(requestContext context.Context, sourceScope *plugin.Scope, subject Agent, destination Status) error {
	return emitScoped(requestContext, sourceScope, subject, statusEvent, StatusNotice{Subject: subject, Status: destination})
}

func EmitInboxInserted(requestContext context.Context, sourceScope *plugin.Scope, subject Agent, message llm.UserMessage) error {
	return emitScoped(requestContext, sourceScope, subject, inboxInsertedEvent, InboxNotice{Subject: subject, Message: message})
}

func EmitInboxClaimed(requestContext context.Context, sourceScope *plugin.Scope, subject Agent, message llm.UserMessage, turn int64) error {
	return emitScoped(requestContext, sourceScope, subject, inboxClaimedEvent, InboxNotice{Subject: subject, Message: message, Turn: turn})
}

func EmitInboxDiscarded(requestContext context.Context, sourceScope *plugin.Scope, subject Agent, message llm.UserMessage) error {
	return emitScoped(requestContext, sourceScope, subject, inboxDiscardedEvent, InboxNotice{Subject: subject, Message: message})
}

func EmitSessionStart(requestContext context.Context, sourceScope *plugin.Scope, subject Agent, source SessionStartSource) error {
	return emitScoped(requestContext, sourceScope, subject, sessionStartEvent, SessionStartNotice{Subject: subject, Source: source})
}

func ResolvePreStep(requestContext context.Context, sourceScope *plugin.Scope, notice PreStepNotice, terminal PreStepNext) (PreStepDecision, error) {
	if notice.Subject == nil || terminal == nil {
		return PreStepDecision{}, errors.New("agent: pre-step subject or terminal is nil")
	}
	return plugin.WaterfallScopedFrom(requestContext, sourceScope, notice.Subject.ScopeValue().Target(), preStepEvent, notice,
		func(chainContext context.Context, _ PreStepNotice) (PreStepDecision, error) {
			return terminal(chainContext)
		})
}

func ResolveRequest(requestContext context.Context, sourceScope *plugin.Scope, notice RequestNotice, terminal RequestNext) (llm.CallConfig, error) {
	if notice.Subject == nil || terminal == nil {
		return llm.CallConfig{}, errors.New("agent: request subject or terminal is nil")
	}
	return plugin.WaterfallScopedFrom(requestContext, sourceScope, notice.Subject.ScopeValue().Target(), requestEvent, notice,
		func(chainContext context.Context, _ RequestNotice) (llm.CallConfig, error) {
			return terminal(chainContext)
		})
}

func ResolveRequestError(requestContext context.Context, sourceScope *plugin.Scope, notice RequestErrorNotice, terminal RequestErrorNext) (RequestErrorAction, error) {
	if notice.Subject == nil || terminal == nil {
		return RequestErrorAction{}, errors.New("agent: request-error subject or terminal is nil")
	}
	return plugin.WaterfallScopedFrom(requestContext, sourceScope, notice.Subject.ScopeValue().Target(), requestErrorEvent, notice,
		func(chainContext context.Context, _ RequestErrorNotice) (RequestErrorAction, error) {
			return terminal(chainContext)
		})
}

func DispatchTurnStopping(requestContext context.Context, sourceScope *plugin.Scope, subject Agent, turn int64) error {
	if subject == nil {
		return errors.New("agent: turn-stopping subject is nil")
	}
	_, err := plugin.SerialScopedFrom(requestContext, sourceScope, subject.ScopeValue().Target(), turnStoppingEvent, TurnNotice{Subject: subject, Turn: turn})
	return err
}

func EmitError(requestContext context.Context, sourceScope *plugin.Scope, notice ErrorNotice) error {
	if notice.Subject == nil {
		return errors.New("agent: error subject is nil")
	}
	return emitScoped(requestContext, sourceScope, notice.Subject, errorEvent, notice)
}
