package middlewareactor

import (
	"errors"
	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agent/middleware"
	goakt "github.com/tochemey/goakt/v4/actor"
)

type preStepCall struct {
	exchange
	business *middleware.PreStep
}

func (call *preStepCall) transport() *exchange     { return &call.exchange }
func (call *preStepCall) cancel(cause error) error { return call.business.Cancel(cause) }
func (call *preStepCall) respond(failure error) {
	call.reply(agent.PreStepResolved{
		Err: failure,
	})
}

func (owner *Grain) resolvePreStep(gc *goakt.GrainContext, input ResolvePreStep) {
	// Routes supplies both execution order and destinations. For example, a
	// registration named "compaction" selects routes["compaction"]; its Target
	// is the Grain identity to invoke, not an address derived from this name.
	selection, routes, err := selectRoutes(input.Routes)
	if err != nil {
		gc.Response(agent.PreStepResolved{
			Err: err,
		})
		return
	}
	business, err := middleware.NewPreStep(selection, input.Input, input.Default)
	if err != nil {
		gc.Response(agent.PreStepResolved{
			Err: err,
		})
		return
	}
	envelope, err := owner.begin(gc, input.CallID, routes)
	if err != nil {
		gc.Response(agent.PreStepResolved{
			Err: err,
		})
		return
	}
	call := &preStepCall{
		exchange: envelope,
		business: business,
	}
	owner.calls[input.CallID] = call
	effect, err := business.Start()
	owner.deliverPreStep(gc, call, effect, err)
}

func (owner *Grain) continuePreStep(gc *goakt.GrainContext, input ContinuePreStep) {
	call, ok := owner.calls[input.CallID].(*preStepCall)
	if !ok || call.failed != nil {
		gc.Response(agent.PreStepResolved{
			Err: errors.New("agent middleware: call is not available"),
		})
		return
	}
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(agent.PreStepResolved{
			Err: errors.New("agent middleware: Continue requires an asynchronous request"),
		})
		return
	}
	effect, err := call.business.Continue(input.Handler)
	if err != nil {
		reply.Response(agent.PreStepResolved{
			Err: err,
		})
		return
	}
	call.downstream[input.Handler] = reply
	owner.deliverPreStep(gc, call, effect, nil)
}

func (call *preStepCall) receive(owner *Grain, gc *goakt.GrainContext, input invoked) {
	result, ok := input.payload.(agent.PreStepResolved)
	failure := input.err
	if failure == nil {
		if !ok {
			failure = errors.New("agent middleware: unexpected pre_step response")
		} else {
			failure = result.Err
		}
	}
	effect, err := call.business.Complete(input.handler, result.Decision, failure)
	owner.deliverPreStep(gc, call, effect, err)
}

func (owner *Grain) deliverPreStep(gc *goakt.GrainContext, call *preStepCall, effect middleware.PreStepEffect, failure error) {
	if failure != nil {
		owner.breakCall(gc, call, failure)
		return
	}
	switch result := effect.(type) {
	case middleware.InvokePreStep:
		owner.invoke(gc, call, result.Handler, InvokePreStep{
			Invocation: Invocation{
				CallID:      call.identity,
				Handler:     result.Handler,
				Coordinator: gc.Self(),
			},
			Input: result.Input,
		})
	case middleware.ReturnPreStep:
		response := agent.PreStepResolved{
			Decision: result.Decision,
			Err:      result.Err,
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
