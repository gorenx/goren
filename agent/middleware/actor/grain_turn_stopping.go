package middlewareactor

import (
	"errors"
	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agent/middleware"
	goakt "github.com/tochemey/goakt/v4/actor"
)

type stoppingCall struct {
	exchange
	business *middleware.TurnStopping
}

func (call *stoppingCall) transport() *exchange     { return &call.exchange }
func (call *stoppingCall) cancel(cause error) error { return call.business.Cancel(cause) }
func (call *stoppingCall) respond(failure error) {
	call.reply(agent.TurnStoppingCompleted{
		Err: failure,
	})
}

func (owner *Grain) runTurnStopping(gc *goakt.GrainContext, input RunTurnStopping) {
	selection, routes, err := selectRoutes(input.Routes)
	if err != nil {
		gc.Response(agent.TurnStoppingCompleted{
			Err: err,
		})
		return
	}
	business, err := middleware.NewTurnStopping(selection, input.Input)
	if err != nil {
		gc.Response(agent.TurnStoppingCompleted{
			Err: err,
		})
		return
	}
	envelope, err := owner.begin(gc, input.CallID, routes)
	if err != nil {
		gc.Response(agent.TurnStoppingCompleted{
			Err: err,
		})
		return
	}
	call := &stoppingCall{
		exchange: envelope,
		business: business,
	}
	owner.calls[input.CallID] = call
	effect, err := business.Start()
	owner.deliverTurnStopping(gc, call, effect, err)
}

func (call *stoppingCall) receive(owner *Grain, gc *goakt.GrainContext, input invoked) {
	result, ok := input.payload.(agent.TurnStoppingCompleted)
	failure := input.err
	if failure == nil {
		if !ok {
			failure = errors.New("agent middleware: unexpected stopping response")
		} else {
			failure = result.Err
		}
	}
	effect, err := call.business.Complete(input.handler, failure)
	owner.deliverTurnStopping(gc, call, effect, err)
}

func (owner *Grain) deliverTurnStopping(gc *goakt.GrainContext, call *stoppingCall, effect middleware.TurnStoppingEffect, failure error) {
	if failure != nil {
		owner.breakCall(gc, call, failure)
		return
	}
	switch result := effect.(type) {
	case middleware.InvokeTurnStopping:
		owner.invoke(gc, call, result.Handler, InvokeTurnStopping{
			Invocation: Invocation{
				CallID:      call.identity,
				Handler:     result.Handler,
				Coordinator: gc.Self(),
			},
			Input: result.Input,
		})
	case middleware.TurnStopped:
		call.respond(result.Err)
		owner.release(call)
	}
}
