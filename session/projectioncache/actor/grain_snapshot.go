package cacheactor

import (
	"errors"

	projectionactor "github.com/gorenx/goren/session/projection/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

func (owner *Grain) cachedSnapshot(gc *goakt.GrainContext, input CachedSnapshot) {
	if owner.worker == nil || input.Header.ID != owner.sessionID || input.Projection == nil {
		gc.Response(Snapshot{
			Err: errors.New("projection cache: snapshot requires initialized storage, matching Session and projection identity"),
		})
		return
	}
	record, found := owner.records.Match(input.Header)
	if !found {
		gc.Response(Snapshot{})
		return
	}
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(Snapshot{
			Err: errors.New("projection cache: snapshot requires an asynchronous request"),
		})
		return
	}
	gc.RequestGrain(input.Projection, projectionactor.ViewCheckpoint{
		Checkpoint: record.Rows,
	}, goakt.WithRequestTimeout(0)).Then(func(payload any, failure error) {
		result := Snapshot{}
		if failure == nil {
			views, ok := payload.(projectionactor.Values)
			if !ok {
				failure = errors.New("projection cache: invalid projection view response")
			} else if views.Err != nil {
				failure = views.Err
			} else {
				result.Value, failure = record.Snapshot(views.Values)
			}
		}
		result.Err = failure
		reply.Response(result)
	})
}
