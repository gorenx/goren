package compactionactor

import (
	"context"
	"errors"
	agentactor "github.com/gorenx/goren/agent/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// admissionFixture records the admission/release contract independently of the
// real AgentLoop tests, which exercise its business state and cancellation.
type admissionFixture struct {
	requestID   string
	holdRelease bool
}

func (*admissionFixture) OnActivate(context.Context, *goakt.GrainProps) error   { return nil }
func (*admissionFixture) OnDeactivate(context.Context, *goakt.GrainProps) error { return nil }
func (owner *admissionFixture) OnReceive(gc *goakt.GrainContext) {
	switch input := gc.Message().(type) {
	case agentactor.BeginMaintenance:
		var failure error
		if owner.requestID != "" || input.CreationID != "creation" || input.Worker == nil || input.RequestID == "" {
			failure = errors.New("invalid maintenance admission")
		} else {
			owner.requestID = input.RequestID
		}
		gc.Response(agentactor.MaintenanceAccepted{
			Err: failure,
		})
	case holdMaintenanceRelease:
		owner.holdRelease = true
		gc.Response(true)
	case agentactor.FinishMaintenance:
		if owner.holdRelease {
			gc.DeferResponse()
			return
		}
		var failure error
		if input.CreationID != "creation" || input.RequestID != owner.requestID {
			failure = errors.New("invalid maintenance release")
		} else {
			owner.requestID = ""
		}
		gc.Response(agentactor.MaintenanceFinished{
			Err: failure,
		})
	case inspectMaintenance:
		gc.Response(owner.requestID)
	default:
		gc.Unhandled()
	}
}

type inspectMaintenance struct{}

// holdMaintenanceRelease leaves the release request unanswered to test uncertainty.
type holdMaintenanceRelease struct{}
