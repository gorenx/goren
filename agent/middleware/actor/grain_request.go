package middlewareactor

import (
	"errors"
	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agent/middleware"
	goakt "github.com/tochemey/goakt/v4/actor"
)

type requestCall struct {
	exchange
	business *middleware.Request
}

func (call *requestCall) transport() *exchange     { return &call.exchange }
func (call *requestCall) cancel(cause error) error { return call.business.Cancel(cause) }
func (call *requestCall) respond(failure error) {
	call.reply(agent.RequestResolved{
		Err: failure,
	})
}

func (owner *Grain) resolveRequest(gc *goakt.GrainContext, input ResolveRequest) {
	selection, routes, err := selectRoutes(input.Routes)
	if err != nil {
		gc.Response(agent.RequestResolved{
			Err: err,
		})
		return
	}
	business, err := middleware.NewRequest(selection, input.Input, input.Default)
	if err != nil {
		gc.Response(agent.RequestResolved{
			Err: err,
		})
		return
	}
	envelope, err := owner.begin(gc, input.CallID, routes)
	if err != nil {
		gc.Response(agent.RequestResolved{
			Err: err,
		})
		return
	}
	call := &requestCall{
		exchange: envelope,
		business: business,
	}
	owner.calls[input.CallID] = call
	effect, err := business.Start()
	owner.deliverRequest(gc, call, effect, err)
}

func (owner *Grain) continueRequest(gc *goakt.GrainContext, input ContinueRequest) {
	call, ok := owner.calls[input.CallID].(*requestCall)
	if !ok || call.failed != nil {
		gc.Response(agent.RequestResolved{
			Err: errors.New("agent middleware: call is not available"),
		})
		return
	}
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(agent.RequestResolved{
			Err: errors.New("agent middleware: Continue requires an asynchronous request"),
		})
		return
	}
	effect, err := call.business.Continue(input.Handler)
	if err != nil {
		reply.Response(agent.RequestResolved{
			Err: err,
		})
		return
	}
	call.downstream[input.Handler] = reply
	owner.deliverRequest(gc, call, effect, nil)
}

func (call *requestCall) receive(owner *Grain, gc *goakt.GrainContext, input invoked) {
	result, ok := input.payload.(agent.RequestResolved)
	failure := input.err
	if failure == nil {
		if !ok {
			failure = errors.New("agent middleware: unexpected request response")
		} else {
			failure = result.Err
		}
	}
	effect, err := call.business.Complete(input.handler, result.Config, failure)
	owner.deliverRequest(gc, call, effect, err)
}

func (owner *Grain) deliverRequest(gc *goakt.GrainContext, call *requestCall, effect middleware.RequestEffect, failure error) {
	if failure != nil {
		owner.breakCall(gc, call, failure)
		return
	}
	switch result := effect.(type) {
	case middleware.InvokeRequest:
		owner.invoke(gc, call, result.Handler, InvokeRequest{
			Invocation: Invocation{
				CallID:      call.identity,
				Handler:     result.Handler,
				Coordinator: gc.Self(),
			},
			Input: result.Input,
		})
	case middleware.ReturnRequest:
		response := agent.RequestResolved{
			Config: result.Config,
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
