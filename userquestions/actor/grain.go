// Package questionactor schedules user questions without blocking a dispatcher.
package questionactor

import (
	"context"
	"errors"

	"github.com/gorenx/goren/userquestions"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

// Initialize binds the immutable business dependencies for this activation.
type Initialize struct {
	Provider userquestions.Provider
	Callers  userquestions.Callers
}

// Ask correlates one question with cancellation. IDs must be unique per invocation.
type Ask struct {
	ID string
	// Context carries local cancellation from a tool task; Actor callers may omit it.
	Context context.Context
	Request userquestions.Request
	// Receiver is used by local tool tasks; Grain callers use the framework reply.
	Receiver ResultReceiver
}

// Cancel interrupts a pending provider wait. Its acknowledgement is not completion.
type Cancel struct {
	ID    string
	Cause error
}

// Result returns the final question answer or its original business error.
type Result struct {
	Answer userquestions.Answer
	Err    error
}

// Completion acknowledges initialization, cancellation or close.
type Completion struct{ Err error }

// Close requires all accepted questions to have settled before release.
type Close struct{}

type pending struct {
	reply  response
	cancel context.CancelCauseFunc
}

type answered struct {
	id     string
	result Result
}

// Grain owns in-flight replies and cancellation handles, never question policy.
type Grain struct {
	service *userquestions.QuestionService
	pending map[string]pending
}

// OnActivate starts without a provider binding.
func (*Grain) OnActivate(context.Context, *goakt.GrainProps) error { return nil }

// OnDeactivate interrupts owned waits and fails unresolved framework replies.
func (owner *Grain) OnDeactivate(context.Context, *goakt.GrainProps) error {
	for _, entry := range owner.pending {
		entry.cancel(context.Canceled)
		entry.reply.send(Result{
			Err: context.Canceled,
		})
	}
	owner.pending = nil
	owner.service = nil
	return nil
}

// OnReceive schedules provider I/O and accepts cancellation while it is pending.
func (owner *Grain) OnReceive(gc *goakt.GrainContext) {
	switch input := gc.Message().(type) {
	case Initialize:
		if owner.service != nil {
			gc.Response(Completion{
				Err: errors.New("userquestions: already initialized"),
			})
			return
		}
		gc.EnableReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll)))
		owner.service = userquestions.New(input.Provider, input.Callers)
		owner.pending = make(map[string]pending)
		gc.Response(Completion{})
	case Ask:
		reply := response{
			receiver: input.Receiver,
		}
		if input.Receiver == nil {
			reply.framework = gc.DeferResponse()
		} else {
			gc.NoErr()
		}
		if owner.service == nil || input.ID == "" {
			reply.send(Result{
				Err: errors.New("userquestions: initialized service and request ID are required"),
			})
			return
		}
		if _, found := owner.pending[input.ID]; found {
			reply.send(Result{
				Err: errors.New("userquestions: request is already pending"),
			})
			return
		}
		requestContext := input.Context
		if requestContext == nil {
			requestContext = context.WithoutCancel(gc.Context())
		}
		ctx, cancel := context.WithCancelCause(requestContext)
		entry := pending{
			reply:  reply,
			cancel: cancel,
		}
		owner.pending[input.ID] = entry
		service := owner.service
		if err := gc.PipeToSelf(func() (any, error) {
			answerValue, failure := service.Ask(ctx, input.Request)
			return answered{
				id: input.ID,
				result: Result{
					Answer: answerValue,
					Err:    failure,
				},
			}, nil
		}); err != nil {
			delete(owner.pending, input.ID)
			cancel(err)
			entry.reply.send(Result{
				Err: err,
			})
		}
	case Cancel:
		if entry, found := owner.pending[input.ID]; found {
			cause := input.Cause
			if cause == nil {
				cause = context.Canceled
			}
			entry.cancel(cause)
		}
		gc.Response(Completion{})
	case answered:
		if entry, found := owner.pending[input.id]; found {
			delete(owner.pending, input.id)
			entry.cancel(nil)
			entry.reply.send(input.result)
		}
		gc.NoErr()
	case Close:
		if len(owner.pending) != 0 {
			gc.Response(Completion{
				Err: errors.New("userquestions: questions are still pending"),
			})
			return
		}
		owner.service = nil
		gc.Response(Completion{})
	default:
		gc.Unhandled()
	}
}

// ResultReceiver delivers one terminal result to a local task, without exposing
// framework envelopes or requiring an artificial timeout for a human answer.
// Deliver must not block the Actor mailbox.
type ResultReceiver interface{ Deliver(Result) }

type response struct {
	framework *goakt.GrainReply
	receiver  ResultReceiver
}

func (target response) send(result Result) {
	if target.receiver != nil {
		target.receiver.Deliver(result)
		return
	}
	target.framework.Response(result)
}
