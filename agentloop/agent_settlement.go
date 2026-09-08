package agentloop

import (
	"encoding/json"
	"errors"

	"github.com/gorenx/goren/agent"
	turnstate "github.com/gorenx/goren/agentloop/internal/turn"
	"github.com/gorenx/goren/session"
)

// settlement retains the exact end fact through commit and durability retries.
// A non-nil barrier means the event committed and only Flush may be retried.
type settlement struct {
	event   TurnEnd
	barrier *session.WriteBarrier
}

// PrepareTurnEnd fixes the conclusion before the first commit attempt. Once the
// event commits, callers must use TurnBarrier instead of appending another end.
func (subject *ReactLoopAgent) PrepareTurnEnd(problem error) (TurnEnd, error) {
	if subject.preparing != nil {
		return TurnEnd{}, errors.New("agentloop: Turn settlement requires Step preparation and start confirmation to finish")
	}
	if subject.current == nil || subject.step != nil {
		return TurnEnd{}, errors.New("agentloop: Turn settlement requires all Steps closed")
	}
	if subject.ending != nil {
		if subject.ending.barrier != nil {
			return TurnEnd{}, errors.New("agentloop: Turn end already committed; Flush is required")
		}
		return subject.ending.event, nil
	}
	candidate := *subject.current
	conclusion := candidate.ResultValue()
	var intent session.TurnCancelCause
	var cancelled agent.Cancellation
	if errors.As(problem, &cancelled) {
		conclusion = turnstate.TurnResultAborted
		intent = cancellationReason(cancelled.Cause)
	} else if problem != nil {
		conclusion = turnstate.TurnResultError
	}
	if conclusion == turnstate.TurnResultNone || conclusion == turnstate.TurnResultContinue {
		conclusion = turnstate.TurnResultError
		problem = errors.New("agentloop: Turn ended without a result")
	}
	if candidate.StateValue() != turnstate.StateSettling || conclusion != candidate.ResultValue() {
		if err := candidate.EnterSettling(conclusion); err != nil {
			return TurnEnd{}, err
		}
	}
	ended, err := candidate.EndValue(problem, intent)
	if err != nil {
		return TurnEnd{}, err
	}
	draft, err := session.NewEventDraft(session.TurnEnded, ended)
	if err != nil {
		return TurnEnd{}, err
	}
	payload, err := json.Marshal(ended)
	if err != nil {
		return TurnEnd{}, err
	}
	subject.current = &candidate
	subject.preparing = nil
	subject.ending = &settlement{
		event: TurnEnd{
			identifier: subject.identifier,
			number:     candidate.Number(),
			draft:      draft,
			payload:    payload,
		},
	}
	return subject.ending.event, nil
}

// ConfirmTurnEnd accepts the exact prepared event and its committed prefix.
// Repeating the same acknowledgement is safe; another range is rejected.
func (subject *ReactLoopAgent) ConfirmTurnEnd(result session.WriteResult) error {
	if subject.ending == nil || subject.current == nil {
		return errors.New("agentloop: Turn end has not been prepared")
	}
	// A successful deduplicated write proves only that no new event was appended.
	// The original end position remains unknown until the log is reconciled.
	if len(result.Events) == 0 {
		return subject.checkCommit(nil, result)
	}
	if err := subject.checkCommit([]session.EventDraft{subject.ending.event.draft}, result); err != nil {
		return err
	}
	if subject.ending.barrier != nil && *subject.ending.barrier != result.Barrier {
		return errors.New("agentloop: Turn end was already committed at another position")
	}
	boundary := result.Barrier
	subject.ending.barrier = &boundary
	return nil
}

// TurnBarrier returns the committed prefix whose durability closes this Turn.
func (subject *ReactLoopAgent) TurnBarrier() (session.WriteBarrier, error) {
	if subject.ending == nil || subject.ending.barrier == nil {
		return session.WriteBarrier{}, errors.New("agentloop: Turn end has not committed")
	}
	return *subject.ending.barrier, nil
}

// ConfirmTurnFlush closes the Turn only after the same Session has durably
// covered its end event. Failure leaves the barrier available for retry.
func (subject *ReactLoopAgent) ConfirmTurnFlush(durable session.WriteBarrier) error {
	boundary, err := subject.TurnBarrier()
	if err != nil {
		return err
	}
	if durable.SessionID != boundary.SessionID || durable.NextSeq < boundary.NextSeq {
		return errors.New("agentloop: Flush does not cover the Turn end")
	}
	if err := subject.current.EnterClosed(); err != nil {
		return err
	}
	subject.current = nil
	subject.ending = nil
	return nil
}

func cancellationReason(intent agent.CancelCause) session.TurnCancelCause {
	switch selected := intent.(type) {
	case nil, agent.UserCancel:
		return session.UserCancelCause{}
	case agent.ParentCancel:
		return session.ParentCancelCause{}
	case agent.DisposedCancel:
		return session.DisposedCancelCause{}
	case agent.HookCancel:
		return session.HookCancelCause{
			Reason: selected.Reason,
		}
	default:
		return session.HookCancelCause{
			Reason: intent.CancelKind(),
		}
	}
}

// ReconcileTurnEnd confirms the fixed end fact from a complete ordered snapshot.
// This is required when a deduplicated commit did not return the original event.
func (subject *ReactLoopAgent) ReconcileTurnEnd(snapshot session.Snapshot) error {
	if subject.ending == nil || subject.current == nil {
		return ErrState
	}
	confirmed, err := subject.ending.event.Find(snapshot)
	if err != nil {
		return err
	}
	if confirmed == nil {
		return errors.New("agentloop: prepared Turn end is absent from Session log")
	}
	return subject.ConfirmTurnEnd(*confirmed)
}
