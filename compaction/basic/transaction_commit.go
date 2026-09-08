package basic

import (
	"errors"

	"github.com/gorenx/goren/compaction"
	"github.com/gorenx/goren/session"
)

// AcceptCommit interprets a decoded Session business reply. Transport failures
// must not enter this method: they leave the original proposal awaiting retry.
func (subject *Transaction) AcceptCommit(written session.WriteResult, failure error) error {
	if failure == nil {
		return subject.confirmWrite(written)
	}
	if len(written.Events) != 0 {
		return errors.Join(failure, errors.New("compaction: commit returned events with an error; original proposal requires confirmation"))
	}
	return subject.rejectWrite(failure)
}

// confirmWrite accepts only the exact pending batch, never a transport success alone.
func (subject *Transaction) confirmWrite(written session.WriteResult) error {
	if subject.stage != StartPending && subject.stage != CommitPending {
		return errors.New("compaction: no transaction commit is pending")
	}
	if written.FirstSeq < 0 || written.NextSeq != written.FirstSeq+int64(len(written.Events)) || len(written.Events) == 0 || written.Barrier.SessionID != subject.sessionID || written.Barrier.NextSeq != written.NextSeq {
		return errors.New("compaction: invalid transaction commit bounds")
	}
	var drafts []session.EventDraft
	switch proposal := subject.write.(type) {
	case *transactionStart:
		draft, err := session.NewEventDraft(compaction.StartEvent, proposal.lifecycle)
		if err != nil {
			return err
		}
		drafts = []session.EventDraft{draft}
	case *transactionCompletion:
		var err error
		drafts, err = proposal.drafts(written.FirstSeq)
		if err != nil {
			return err
		}
	case *transactionEnd:
		draft, err := proposal.draft()
		if err != nil {
			return err
		}
		drafts = []session.EventDraft{draft}
	default:
		return errors.New("compaction: unknown transaction proposal")
	}
	if len(drafts) != len(written.Events) {
		return errors.New("compaction: incomplete transaction commit")
	}
	for index, draft := range drafts {
		if written.Events[index].Seq != written.FirstSeq+int64(index) {
			return errors.New("compaction: noncontiguous transaction commit")
		}
		if err := draft.CheckCommit(written.Events[index]); err != nil {
			return err
		}
	}
	if subject.stage == StartPending {
		subject.started = written.Events[0]
		subject.started.Data = append([]byte(nil), subject.started.Data...)
		subject.write = nil
		subject.stage = SummaryPending
		if subject.cancellation != nil {
			return subject.Cancel(subject.cancellation)
		}
		return nil
	}
	if subject.failure == nil {
		generated, err := compaction.DecodeSummary(written.Events[0].Data)
		if err != nil {
			return err
		}
		subject.result = &compaction.Result{
			CompactionID:       subject.start.lifecycle.CompactionID,
			SourceCommandID:    cloneString(subject.start.lifecycle.SourceCommandID),
			StartSeq:           subject.started.Seq,
			SummarySeq:         written.FirstSeq,
			EndSeq:             written.NextSeq - 1,
			Summary:            generated.Summary,
			ShadowedRange:      generated.ShadowedRange,
			ShadowedSeqs:       generated.ShadowedSeqs,
			ShadowedTokenCount: generated.ShadowedTokenCount,
		}
	}
	subject.barrier = written.Barrier
	subject.write = nil
	subject.stage = TransactionFinished
	if subject.start.lifecycle.Turn == nil {
		subject.stage = FlushPending
	}
	return nil
}

// rejectWrite consumes a definitive business rejection, never an unknown commit.
// A rejected failed end stays pending; the caller must retry or report unfinished work.
func (subject *Transaction) rejectWrite(failure error) error {
	if failure == nil {
		return errors.New("compaction: commit rejection requires a cause")
	}
	switch subject.stage {
	case StartPending:
		subject.failure = failure
		subject.write = nil
		subject.stage = TransactionFinished
		return nil
	case CommitPending:
		if subject.failure != nil {
			return errors.Join(subject.failure, failure)
		}
		ending, err := newTransactionEnd(subject.start.lifecycle, failure)
		if err != nil {
			return err
		}
		subject.failure = failure
		subject.write = ending
		return nil
	default:
		return errors.New("compaction: no transaction commit is pending")
	}
}
