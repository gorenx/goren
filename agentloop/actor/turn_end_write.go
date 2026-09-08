package loopactor

import (
	"context"

	"github.com/gorenx/goren/agentloop"
	"github.com/gorenx/goren/session"
)

// turnEndWrite evaluates the detached business end inside Session's Commit.
// It never references the Grain or mutable Loop across owner boundaries.
type turnEndWrite struct {
	event agentloop.TurnEnd
}

func (plan turnEndWrite) Build(_ context.Context, snapshot session.Snapshot) ([]session.EventDraft, error) {
	return plan.event.Drafts(snapshot)
}

// FindCommit lets Session return the original end acknowledgement on retry.
func (plan turnEndWrite) FindCommit(snapshot session.Snapshot) (*session.WriteResult, error) {
	return plan.event.Find(snapshot)
}
