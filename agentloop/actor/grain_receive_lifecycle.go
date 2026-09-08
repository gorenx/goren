package loopactor

import (
	"errors"
	agentactor "github.com/gorenx/goren/agent/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Lifecycle messages remain independent of execution progress: cancellation delivers intent; maintenance release requires a matching completion.
func (owner *Grain) receiveLifecycle(gc *goakt.GrainContext) bool {
	switch input := gc.Message().(type) {
	case agentactor.BeginMaintenance:
		owner.beginMaintenance(gc, input)
	case agentactor.FinishMaintenance:
		owner.finishMaintenance(gc, input)
	case completeCancel:
		owner.finishCancellation(gc, input)
	case Cancel:
		owner.cancel(gc, input)
	case WhenIdle:
		owner.whenIdle(gc, input)
	case idleObserved:
		owner.confirmIdle(gc, input)
	case agentactor.Verify:
		failure := owner.checkCreation(input.CreationID)
		if failure == nil && (input.InstanceID == "" || input.InstanceID != owner.instanceID) {
			failure = errors.New("agentloop: activation is not current")
		}
		gc.Response(agentactor.Verified{
			Err: failure,
		})
	case agentactor.PrepareClose:
		gc.Response(agentactor.Prepared{
			Err: owner.prepareClose(input.CreationID),
		})
	case agentactor.CloseResources:
		owner.closeResources(gc, input)
	default:
		return false
	}
	return true
}
