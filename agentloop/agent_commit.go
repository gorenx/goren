package agentloop

import (
	"context"
	"errors"
	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
)

// ConfirmEdit validates the entire acknowledgement before updating the Inbox.
// A mismatched Session, batch, or sequence leaves the projection unchanged.
func (subject *ReactLoopAgent) ConfirmEdit(edit agent.InboxEdit, write session.WriteResult) (agent.InboxChanges, error) {
	if err := subject.checkCommit(edit.Drafts, write); err != nil {
		return agent.InboxChanges{}, err
	}
	return subject.Apply(write.Events)
}

func (subject *ReactLoopAgent) checkCommit(drafts []session.EventDraft, write session.WriteResult) error {
	if write.Barrier.SessionID != subject.identifier || write.FirstSeq < 0 ||
		write.NextSeq != write.FirstSeq+int64(len(drafts)) ||
		write.Barrier.NextSeq != write.NextSeq || len(write.Events) != len(drafts) {
		return errors.New("agentloop: invalid commit range or Session identity")
	}
	for index, draft := range drafts {
		if write.Events[index].Seq != write.FirstSeq+int64(index) {
			return errors.New("agentloop: commit sequence is discontinuous")
		}
		if err := draft.ValidateEvent(write.Events[index]); err != nil {
			return err
		}
	}
	return nil
}

// boundary is implemented by immutable TurnStart, EndStep and TurnEnd facts.
type boundary interface {
	Drafts(session.Snapshot) ([]session.EventDraft, error)
	Find(session.Snapshot) (*session.WriteResult, error)
}

// boundaryWrite crosses the Session boundary without a reference to execution.
type boundaryWrite struct{ fact boundary }

func (plan boundaryWrite) Build(_ context.Context, snapshot session.Snapshot) ([]session.EventDraft, error) {
	return plan.fact.Drafts(snapshot)
}

func (plan boundaryWrite) FindCommit(snapshot session.Snapshot) (*session.WriteResult, error) {
	return plan.fact.Find(snapshot)
}
