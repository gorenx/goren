package middlewareactor

import (
	"errors"
	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agent/middleware"
	goakt "github.com/tochemey/goakt/v4/actor"
)

type requestErrorCall struct {
	exchange
	business *middleware.RequestError
}

func (call *requestErrorCall) transport() *exchange     { return &call.exchange }
func (call *requestErrorCall) cancel(cause error) error { return call.business.Cancel(cause) }
func (call *requestErrorCall) respond(failure error) {
	call.reply(agent.RequestErrorResolved{
		Err: failure,
	})
}

func (owner *Grain) resolveRequestError(gc *goakt.GrainContext, input ResolveRequestError) {
	selection, routes, err := selectRoutes(input.Routes)
	if err != nil {
		gc.Response(agent.RequestErrorResolved{
			Err: err,
		})
		return
	}
	business, err := middleware.NewRequestError(selection, input.Input)
	if err != nil {
		gc.Response(agent.RequestErrorResolved{
			Err: err,
		})
		return
	}
	envelope, err := owner.begin(gc, input.CallID, routes)
	if err != nil {
		gc.Response(agent.RequestErrorResolved{
			Err: err,
		})
		return
	}
	call := &requestErrorCall{
		exchange: envelope,
		business: business,
	}
	owner.calls[input.CallID] = call
	effect, err := business.Start()
	owner.deliverRequestError(gc, call, effect, err)
}

func (owner *Grain) continueRequestError(gc *goakt.GrainContext, input ContinueRequestError) {
	call, ok := owner.calls[input.CallID].(*requestErrorCall)
	if !ok || call.failed != nil {
		gc.Response(agent.RequestErrorResolved{
			Err: errors.New("agent middleware: call is not available"),
		})
		return
	}
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(agent.RequestErrorResolved{
			Err: errors.New("agent middleware: Continue requires an asynchronous request"),
		})
		return
	}
	effect, err := call.business.Continue(input.Handler)
	if err != nil {
		reply.Response(agent.RequestErrorResolved{
			Err: err,
		})
		return
	}
	call.downstream[input.Handler] = reply
	owner.deliverRequestError(gc, call, effect, nil)
}

func (call *requestErrorCall) receive(owner *Grain, gc *goakt.GrainContext, input invoked) {
	result, ok := input.payload.(agent.RequestErrorResolved)
	failure := input.err
	if failure == nil {
		if !ok {
			failure = errors.New("agent middleware: unexpected request_error response")
		} else {
			failure = result.Err
		}
	}
	effect, err := call.business.Complete(input.handler, result.Action, failure)
	owner.deliverRequestError(gc, call, effect, err)
}

func (owner *Grain) deliverRequestError(gc *goakt.GrainContext, call *requestErrorCall, effect middleware.RequestErrorEffect, failure error) {
	if failure != nil {
		owner.breakCall(gc, call, failure)
		return
	}
	switch result := effect.(type) {
	case middleware.InvokeRequestError:
		owner.invoke(gc, call, result.Handler, InvokeRequestError{
			Invocation: Invocation{
				CallID:      call.identity,
				Handler:     result.Handler,
				Coordinator: gc.Self(),
			},
			Input: result.Input,
		})
	case middleware.ReturnRequestError:
		response := agent.RequestErrorResolved{
			Action: result.Action,
			Err:    result.Err,
		}
		if result.To == "" {
			call.root.Response(response)
			call.root = nil
			owner.release(call)
			return
		}
		reply := call.downstream[result.To]
		if reply == nil {
			owner.breakCall(gc, call, errors.New("agent middleware: missing downstream request"))
			return
		}
		delete(call.downstream, result.To)
		reply.Response(response)
	}
}
