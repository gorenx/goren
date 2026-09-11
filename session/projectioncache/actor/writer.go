package cacheactor

import (
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/projectioncache"
	goakt "github.com/tochemey/goakt/v4/actor"
)

type write struct {
	record projectioncache.CheckpointRecord
}

// writer performs storage effects for one Session. It owns no checkpoint policy.
// Each Session uses an independent mailbox; the backend remains composition-owned.
type writer struct {
	identifier session.SessionID
	backend    projectioncache.CheckpointStore
}

func (*writer) PreStart(*goakt.Context) error { return nil }
func (*writer) PostStop(*goakt.Context) error { return nil }
func (worker *writer) Receive(rc *goakt.ReceiveContext) {
	switch input := rc.Message().(type) {
	case write:
		rc.Response(Completion{
			Err: worker.backend.Replace(rc.Context(), worker.identifier, input.record),
		})
	default:
		rc.Unhandled()
	}
}
