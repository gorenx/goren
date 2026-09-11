package promptactor

import (
	"errors"

	goakt "github.com/tochemey/goakt/v4/actor"
)

func (owner *RuntimeGrain) applyConfiguration(gc *goakt.GrainContext, input ApplyConfiguration) {
	if owner.runtime == nil ||
		owner.closed ||
		owner.closing != nil {
		gc.Response(Completion{
			Err: ErrUnavailable,
		})
		return
	}
	if owner.configuration != nil {
		gc.Response(Completion{
			Err: ErrBusy,
		})
		return
	}
	candidate := *owner.runtime
	if err := candidate.ApplyTools(input.AgentID, input.Definitions); err != nil {
		gc.Response(Completion{
			Err: err,
		})
		return
	}
	retained := gc.DeferResponse()
	if retained == nil {
		gc.Response(Completion{
			Err: ErrAsyncRequest,
		})
		return
	}
	owner.configuration = retained
	gc.RequestGrain(owner.registry, Prepare{}, goakt.WithRequestTimeout(goakt.DefaultGrainRequestTimeout)).Then(func(payload any, failure error) {
		if owner.configuration != retained {
			return
		}
		result, valid := payload.(Prepared)
		if failure == nil {
			switch {
			case !valid:
				failure = errors.New("systemprompt: invalid registration snapshot response")
			case result.Err != nil:
				failure = result.Err
			case result.Version < owner.version:
				failure = errors.New("systemprompt: stale registration snapshot")
			}
		}
		if failure == nil {
			owner.layer.SetBase(result.Snapshot)
			candidate.SetDefinitions(owner.layer.Capture())
			owner.runtime = &candidate
			owner.version = result.Version
		}
		owner.configuration = nil
		retained.Response(Completion{
			Err: failure,
		})
	})
}
