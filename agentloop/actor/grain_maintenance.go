package loopactor

import (
	"errors"
	agentactor "github.com/gorenx/goren/agent/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// maintenance retains only the address and identity needed to deliver cancellation.
// ReactLoopAgent owns admission, cancellation intent and wake decisions.
type maintenance struct {
	requestID string
	worker    *goakt.GrainIdentity
}

func (owner *Grain) beginMaintenance(gc *goakt.GrainContext, input agentactor.BeginMaintenance) {
	failure := owner.checkCreation(input.CreationID)
	if failure == nil && (input.RequestID == "" || input.Worker == nil) {
		failure = errors.New("agentloop: maintenance requires a request identity and worker")
	}
	if failure == nil {
		failure = owner.business.BeginMaintenance()
	}
	if failure == nil {
		owner.maintenance = &maintenance{
			requestID: input.RequestID,
			worker:    input.Worker,
		}
	}
	gc.Response(agentactor.MaintenanceAccepted{
		Err: failure,
	})
}

func (owner *Grain) finishMaintenance(gc *goakt.GrainContext, input agentactor.FinishMaintenance) {
	failure := owner.checkCreation(input.CreationID)
	if failure == nil && (owner.maintenance == nil || owner.maintenance.requestID != input.RequestID) {
		failure = errors.New("agentloop: maintenance request is not current")
	}
	if failure == nil {
		failure = owner.business.MaintenanceCompleted(owner.services(gc))
		if failure == nil {
			owner.maintenance = nil
		}
	}
	gc.Response(agentactor.MaintenanceFinished{
		Err: failure,
	})
}
