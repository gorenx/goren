package loopactor

import (
	"context"

	"github.com/gorenx/goren/agentloop"
	"github.com/gorenx/goren/session"
)

// turnStartWrite evaluates a detached Turn start inside Session's transaction.
type turnStartWrite struct {
	event agentloop.TurnStart
}

func (plan turnStartWrite) Build(_ context.Context, snapshot session.Snapshot) ([]session.EventDraft, error) {
	return plan.event.Drafts(snapshot)
}
func (plan turnStartWrite) FindCommit(snapshot session.Snapshot) (*session.WriteResult, error) {
	return plan.event.Find(snapshot)
}
