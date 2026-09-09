package toolresultpruner

import (
	"context"
	"errors"
	"fmt"

	"github.com/gorenx/goren/session"
)

// Plan fixes one atomic pruning batch and its candidate result at a log revision.
// Result describes committed changes only after Session confirms this exact plan.
type Plan struct {
	barrier session.WriteBarrier
	drafts  []session.EventDraft
	result  Result
}

// NewPlan computes detached drafts once, before crossing the Session boundary.
func (impl *ToolResultPruner) NewPlan(snapshot session.Snapshot, pricing Estimator) (*Plan, error) {
	if impl == nil || pricing == nil {
		return nil, errors.New("toolresultpruner: pruner and estimator are required")
	}
	if snapshot.Barrier.SessionID == "" || snapshot.Barrier.NextSeq != int64(len(snapshot.Events)) {
		return nil, errors.New("toolresultpruner: coherent Session snapshot is required")
	}
	drafts, outcome, err := impl.prepare(snapshot, pricing)
	if err != nil {
		return nil, err
	}
	return &Plan{
		barrier: snapshot.Barrier,
		drafts:  drafts,
		result:  outcome,
	}, nil
}

// Result returns independent bookkeeping for the fixed candidate batch.
func (proposal *Plan) Result() Result {
	return Result{
		Pruned:       append([]Entry(nil), proposal.result.Pruned...),
		CharsRemoved: proposal.result.CharsRemoved,
	}
}

// Build rejects stale preparation instead of repricing a different Surface.
func (proposal *Plan) Build(_ context.Context, snapshot session.Snapshot) ([]session.EventDraft, error) {
	if snapshot.Barrier != proposal.barrier {
		return nil, errors.New("toolresultpruner: pruning snapshot changed before commit")
	}
	return append([]session.EventDraft(nil), proposal.drafts...), nil
}

// FindCommit confirms the original batch without recalculating pruning results.
func (proposal *Plan) FindCommit(snapshot session.Snapshot) (*session.WriteResult, error) {
	if snapshot.Barrier.SessionID != proposal.barrier.SessionID || snapshot.Barrier.NextSeq != int64(len(snapshot.Events)) || snapshot.Barrier.NextSeq < proposal.barrier.NextSeq {
		return nil, errors.New("toolresultpruner: incompatible Session snapshot")
	}
	first := proposal.barrier.NextSeq
	if len(proposal.drafts) == 0 || snapshot.Barrier.NextSeq == first {
		return nil, nil
	}
	next := first + int64(len(proposal.drafts))
	if next > snapshot.Barrier.NextSeq {
		return nil, errors.New("toolresultpruner: original batch is incomplete or its position was occupied")
	}
	for index, draft := range proposal.drafts {
		entry := snapshot.Events[first+int64(index)]
		if entry.Seq != first+int64(index) {
			return nil, errors.New("toolresultpruner: original batch is not contiguous")
		}
		if err := draft.ValidateEvent(entry); err != nil {
			return nil, fmt.Errorf("toolresultpruner: original batch conflicts with committed history: %w", err)
		}
	}
	return &session.WriteResult{
		FirstSeq: first,
		NextSeq:  next,
		Barrier: session.WriteBarrier{
			SessionID: proposal.barrier.SessionID,
			NextSeq:   next,
		},
		Events: snapshot.Events[first:next],
	}, nil
}

var _ session.IdempotentWrite = (*Plan)(nil)
