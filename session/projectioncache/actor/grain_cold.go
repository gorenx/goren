package cacheactor

import (
	"errors"

	"github.com/google/uuid"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

func (owner *Grain) coldSnapshot(gc *goakt.GrainContext, input ColdSnapshot) {
	if owner.worker == nil || input.Storage == nil || input.Projection == nil {
		gc.Response(ColdView{
			Err: errors.New("projection cache: cold read requires initialized cache, storage and projection"),
		})
		return
	}
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(ColdView{
			Err: errors.New("projection cache: cold read requires an asynchronous request"),
		})
		return
	}
	task := &reader{
		identifier:  owner.sessionID,
		backend:     input.Storage,
		definitions: input.Projection,
		reply:       reply,
	}
	if record, found := owner.records.Snapshot(task.identifier); found {
		task.initial = &record
	}
	_, err := gc.ActorSystem().Spawn(gc.Context(), "checkpoint-reader-"+uuid.NewString(), task,
		goakt.WithReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		reply.Response(ColdView{
			Err: err,
		})
	}
}
