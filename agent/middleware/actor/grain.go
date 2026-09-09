package middlewareactor

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent/middleware"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

type execution interface {
	transport() *exchange
	cancel(error) error
	respond(error)
	receive(*Grain, *goakt.GrainContext, invoked)
}

// exchange contains transport correlation only, never middleware order decisions.
type exchange struct {
	identity CallID
	root     *goakt.GrainReply
	// downstream maps suspended handlers to their outstanding Continue replies.
	downstream map[middleware.RegistrationID]*goakt.GrainReply
	// routes maps caller-supplied registration IDs to their module Grains for
	// this invocation, e.g. routes["compaction"] == compactGrain. invoke uses
	// this map to send InvokePreStep; the ID itself is not an actor address.
	routes map[middleware.RegistrationID]*goakt.GrainIdentity
	// active contains requests whose module work has not replied yet.
	active  map[middleware.RegistrationID]bool
	context context.Context
	stop    context.CancelFunc
	failed  error
}

func (call *exchange) reply(payload any) {
	if call.root != nil {
		call.root.Response(payload)
		call.root = nil
	}
	for identity, reply := range call.downstream {
		reply.Response(payload)
		delete(call.downstream, identity)
	}
}

// Grain executes fixed middleware selections with reentrant asynchronous requests.
// It must be activated with GoAkt reentrancy enabled. Definition ownership and
// registration updates belong to the caller's registry, not this adapter.
type Grain struct {
	stopped chan struct{}
	// calls maps a caller-generated invocation ID to its live transport exchange.
	calls map[CallID]execution
}

// OnActivate starts without reviving process-local calls.
func (owner *Grain) OnActivate(context.Context, *goakt.GrainProps) error {
	owner.stopped = make(chan struct{})
	return nil
}

// OnDeactivate fails outstanding requests without asserting module cleanup.
func (owner *Grain) OnDeactivate(context.Context, *goakt.GrainProps) error {
	close(owner.stopped)
	for _, call := range owner.calls {
		call.transport().stop()
		call.respond(errors.New("agent middleware: coordinator stopped during invocation"))
	}
	owner.calls = nil
	return nil
}

// OnReceive maps messages into pure business calls and transports their effects.
func (owner *Grain) OnReceive(gc *goakt.GrainContext) {
	switch input := gc.Message().(type) {
	case ResolvePreStep:
		owner.resolvePreStep(gc, input)
	case ContinuePreStep:
		owner.continuePreStep(gc, input)
	case ResolveRequest:
		owner.resolveRequest(gc, input)
	case ContinueRequest:
		owner.continueRequest(gc, input)
	case ResolveRequestError:
		owner.resolveRequestError(gc, input)
	case ContinueRequestError:
		owner.continueRequestError(gc, input)
	case RunTurnStopping:
		owner.runTurnStopping(gc, input)
	case CancelCall:
		call, found := owner.calls[input.CallID]
		if !found || input.Cause == nil {
			gc.Response(Completion{
				Err: errors.New("agent middleware: cancellation requires a live call and cause"),
			})
			return
		}
		reply := gc.DeferResponse()
		if reply == nil {
			gc.Response(Completion{
				Err: errors.New("agent middleware: CancelCall requires an asynchronous request"),
			})
			return
		}
		cause := call.cancel(input.Cause)
		owner.notifyCancellation(gc, call, cause, reply)
	case completeCancel:
		if call := owner.calls[input.envelope.identity]; input.reply == nil && call != nil && call.transport() == input.envelope {
			input.envelope.failed = errors.Join(input.envelope.failed, input.err)
		}
		if input.reply != nil {
			input.reply.Response(Completion{
				Err: input.err,
			})
		}
	case invoked:
		call := owner.calls[input.identity]
		if call == nil || call.transport() != input.exchange {
			return
		}
		delete(call.transport().active, input.handler)
		if call.transport().failed != nil {
			if len(call.transport().active) == 0 {
				call.respond(call.transport().failed)
				owner.release(call)
			}
			return
		}
		call.receive(owner, gc, input)
	default:
		gc.Unhandled()
	}
}

func selectRoutes(entries []Route) ([]middleware.RegistrationID, map[middleware.RegistrationID]*goakt.GrainIdentity, error) {
	selection := make([]middleware.RegistrationID, len(entries))
	routes := make(map[middleware.RegistrationID]*goakt.GrainIdentity, len(entries))
	for index, entry := range entries {
		if entry.Definition == "" || entry.Target == nil {
			return nil, nil, errors.New("agent middleware: incomplete route")
		}
		if err := entry.Target.Validate(); err != nil {
			return nil, nil, err
		}
		if _, found := routes[entry.Definition]; found {
			return nil, nil, errors.New("agent middleware: duplicate route")
		}
		selection[index] = entry.Definition
		routes[entry.Definition] = entry.Target
	}
	return selection, routes, nil
}

func (owner *Grain) begin(
	gc *goakt.GrainContext,
	identity CallID,
	routes map[middleware.RegistrationID]*goakt.GrainIdentity,
) (exchange, error) {
	if identity == "" || owner.calls[identity] != nil {
		return exchange{}, errors.New("agent middleware: invocation ID is empty or already active")
	}
	reply := gc.DeferResponse()
	if reply == nil {
		return exchange{}, errors.New("agent middleware: Resolve requires an asynchronous request")
	}
	if owner.calls == nil {
		owner.calls = make(map[CallID]execution)
	}
	ctx, stop := context.WithCancel(context.WithoutCancel(gc.Context()))
	return exchange{
		identity:   identity,
		root:       reply,
		downstream: make(map[middleware.RegistrationID]*goakt.GrainReply),
		routes:     routes,
		active:     make(map[middleware.RegistrationID]bool),
		context:    ctx,
		stop:       stop,
	}, nil
}

type invoked struct {
	identity CallID
	exchange *exchange
	handler  middleware.RegistrationID
	payload  any
	err      error
}

func (owner *Grain) invoke(gc *goakt.GrainContext, call execution, identity middleware.RegistrationID, message any) {
	envelope := call.transport()
	completed := make(chan invoked, 1)
	if err := gc.PipeToSelf(func() (any, error) {
		select {
		case result := <-completed:
			return result, nil
		case <-envelope.context.Done():
			return nil, context.Cause(envelope.context)
		}
	}); err != nil {
		owner.breakCall(gc, call, err)
		return
	}
	envelope.active[identity] = true
	// The callback only captures detached routing data and a channel. GrainContext
	// is pooled and must never be retained by a delayed continuation.
	gc.RequestGrain(envelope.routes[identity], message,
		goakt.WithRequestTimeout(0),
		goakt.WithReentrancyMode(reentrancy.AllowAll),
	).Then(func(payload any, failure error) {
		completed <- invoked{
			identity: envelope.identity,
			exchange: envelope,
			handler:  identity,
			payload:  payload,
			err:      failure,
		}
	})
}

func (owner *Grain) breakCall(gc *goakt.GrainContext, call execution, cause error) {
	envelope := call.transport()
	envelope.failed = errors.Join(envelope.failed, cause)
	effective := call.cancel(cause)
	owner.notifyCancellation(gc, call, effective, nil)
	// Reply to suspended Continue requests so handlers can unwind, but retain the
	// root and live module requests until their original invocations have replied.
	root := envelope.root
	envelope.root = nil
	call.respond(envelope.failed)
	envelope.root = root
	if len(envelope.active) == 0 {
		call.respond(envelope.failed)
		owner.release(call)
	}
}

func (owner *Grain) release(call execution) {
	envelope := call.transport()
	envelope.stop()
	delete(owner.calls, envelope.identity)
}
