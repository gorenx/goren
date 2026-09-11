package compactionactor

import (
	"errors"

	middlewareactor "github.com/gorenx/goren/agent/middleware/actor"
	llmactor "github.com/gorenx/goren/llm/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

func (owner *Grain) cancelHook(gc *goakt.GrainContext, input middlewareactor.CancelInvocation) {
	current := owner.active
	if current == nil || current.hook == nil || current.hook.CallID != input.CallID || current.hook.Handler != input.Handler {
		// A late cancellation cannot affect a different invocation on this Grain.
		gc.Response(middlewareactor.Completion{})
		return
	}
	gc.Response(middlewareactor.Completion{
		Err: owner.cancelInvocation(gc, current, input.Cause),
	})
}

// OnCancellationAccepted handles delivery only; the original stream operation
// and Close reply still drive transaction settlement through the business owner.
func (owner *Grain) OnCancellationAccepted(gc *goakt.GrainContext, result llmactor.CancellationAccepted) {
	current := owner.active
	if current == nil || current.callID != result.CallID {
		return
	}
	owner.report(gc, result.Err)
}

func (owner *Grain) cancel(gc *goakt.GrainContext, input Cancel) {
	current := owner.active
	if current == nil || current.hook != nil || current.input.RequestID != input.RequestID {
		gc.Response(Completion{})
		return
	}
	gc.Response(Completion{
		Err: owner.cancelInvocation(gc, current, input.Cause),
	})
}

func (owner *Grain) cancelInvocation(gc *goakt.GrainContext, current *invocation, cause error) error {
	if cause == nil {
		return errors.New("compaction: cancellation requires a cause")
	}
	if current.cancelled != nil {
		return nil
	}
	if err := owner.business.Cancel(cause); err != nil {
		return err
	}
	current.cancelled = cause
	if current.callID != "" {
		owner.models.Cancel(gc, current.callID, cause, owner)
	}
	return nil
}
