package compactionactor

import (
	"errors"

	"github.com/gorenx/goren/agent"
	middlewareactor "github.com/gorenx/goren/agent/middleware/actor"
	"github.com/gorenx/goren/compaction/basic"
	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

func (owner *Grain) admitHook(address middlewareactor.Invocation, identifier session.SessionID) error {
	if owner.business == nil ||
		owner.commandClosing() ||
		owner.active != nil ||
		identifier != owner.agentID ||
		address.Coordinator == nil ||
		address.CallID == "" ||
		address.Handler == "" {
		return errors.New("compaction: idle initialized owner and matching middleware identity are required")
	}
	return nil
}

func (owner *Grain) startPressure(gc *goakt.GrainContext, input middlewareactor.InvokePreStep) {
	if err := owner.admitHook(input.Invocation, input.Input.AgentID); err != nil {
		gc.Response(agent.PreStepResolved{
			Err: err,
		})
		return
	}
	current := &invocation{
		reply: gc.DeferResponse(),
		input: Execute{
			Mode: basic.Pressure,
		},
		hook: &input.Invocation,
	}
	owner.active = current
	owner.prepare(gc, current)
}

func (owner *Grain) startRecovery(gc *goakt.GrainContext, input middlewareactor.InvokeRequestError) {
	if err := owner.admitHook(input.Invocation, input.Input.AgentID); err != nil {
		gc.Response(agent.RequestErrorResolved{
			Err: err,
		})
		return
	}
	current := &invocation{
		reply: gc.DeferResponse(),
		input: Execute{
			Mode: basic.Overflow,
		},
		hook: &input.Invocation,
	}
	owner.active = current
	current.failure = input.Input
	owner.request(gc, current, owner.conversation, sessionactor.Read{
		CreationID: owner.creationID,
	}, recoveryHistory)
}

// reject handles preflight failure before a Compaction invocation has started.
func (owner *Grain) reject(gc *goakt.GrainContext, current *invocation, failure error) {
	if current.hook == nil {
		owner.finish(gc, current, basic.Outcome{}, failure)
		return
	}
	if current.input.Mode == basic.Overflow {
		if err := owner.recovery.Reject(); err != nil {
			failure = errors.Join(failure, err)
		}
		owner.report(gc, failure)
	} else {
		owner.report(gc, owner.diagnostics.Pressure(failure))
	}
	owner.continueHook(gc, current)
}

func (owner *Grain) completeHook(gc *goakt.GrainContext, current *invocation, failure error) {
	if current.input.Mode == basic.Pressure {
		owner.report(gc, owner.diagnostics.Pressure(failure))
		owner.continueHook(gc, current)
		return
	}
	decision, err := owner.recovery.Complete(owner.business, current.cancelled)
	if err != nil {
		owner.report(gc, err)
		owner.continueHook(gc, current)
		return
	}
	owner.report(gc, decision.Diagnostic)
	if !decision.Retry {
		owner.continueHook(gc, current)
		return
	}
	owner.active = nil
	current.reply.Response(agent.RequestErrorResolved{
		Action: agent.RequestErrorAction{
			Retry: true,
		},
	})
}

func (*Grain) report(gc *goakt.GrainContext, failure error) {
	if failure != nil {
		gc.ActorSystem().Logger().Warn(failure.Error())
	}
}

func (owner *Grain) continueHook(gc *goakt.GrainContext, current *invocation) {
	var message any
	if current.input.Mode == basic.Pressure {
		message = middlewareactor.ContinuePreStep{
			CallID:  current.hook.CallID,
			Handler: current.hook.Handler,
		}
	} else {
		message = middlewareactor.ContinueRequestError{
			CallID:  current.hook.CallID,
			Handler: current.hook.Handler,
		}
	}
	// The coordinator owns downstream invocation and its cancellation/settlement.
	// A deadline here must not release the original Invoke while downstream runs.
	gc.RequestGrain(current.hook.Coordinator, message, goakt.WithRequestTimeout(0), goakt.WithReentrancyMode(reentrancy.AllowAll)).Then(func(payload any, failure error) {
		if owner.active != current {
			return
		}
		owner.active = nil
		if current.input.Mode == basic.Pressure {
			result, ok := payload.(agent.PreStepResolved)
			if failure == nil && !ok {
				failure = errors.New("compaction: invalid downstream PreStep result")
			}
			result.Err = errors.Join(result.Err, failure)
			current.reply.Response(result)
		} else {
			result, ok := payload.(agent.RequestErrorResolved)
			if failure == nil && !ok {
				failure = errors.New("compaction: invalid downstream RequestError result")
			}
			result.Err = errors.Join(result.Err, failure)
			current.reply.Response(result)
		}
	})
}
