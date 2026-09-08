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
	if input.Cause == nil {
		gc.Response(middlewareactor.Completion{
			Err: errors.New("compaction: cancellation requires a cause"),
		})
		return
	}
	if current.cancelled != nil {
		gc.Response(middlewareactor.Completion{})
		return
	}
	current.cancelled = input.Cause
	if err := owner.business.Cancel(input.Cause); err != nil {
		gc.Response(middlewareactor.Completion{
			Err: err,
		})
		return
	}
	gc.Response(middlewareactor.Completion{})
	if current.callID != "" {
		owner.models.Cancel(gc, current.callID, current.cancelled, owner)
	}
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
