package turn

import (
	"encoding/json"
	"errors"
	"fmt"

	stepstate "github.com/gorenx/goren/agentloop/internal/step"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

// LastNumber restores the most recent durable turn/start number from a detached log.
// It does not infer successful completion or resume an interrupted execution.
func LastNumber(entries []session.Event) (int64, error) {
	for index := len(entries) - 1; index >= 0; index-- {
		if entries[index].Type != session.TurnStartEventName {
			continue
		}
		var started session.TurnStart
		if err := json.Unmarshal(entries[index].Data, &started); err != nil || started.Turn <= 0 || started.Turn > maxNumber {
			return 0, fmt.Errorf("agentloop: invalid persisted turn/start at seq %d", entries[index].Seq)
		}
		return started.Turn, nil
	}
	return 0, nil
}

// StartDraft describes the proposed durable boundary without opening the Turn.
// The caller must commit it before invoking EnterOpen.
func (current *Turn) StartDraft() (session.EventDraft, error) {
	if current == nil {
		return session.EventDraft{}, errors.New("agentloop turn: Turn is nil")
	}
	if current.currentState != StateProposed {
		return session.EventDraft{}, current.transitionError("prepare start")
	}
	return session.NewEventDraft(
		session.TurnStarted,
		session.TurnStart{
			Turn: current.number,
		},
	)
}

// EndDraft encodes the accepted terminal conclusion without closing the Turn.
// The caller must confirm both the event commit and its flush before EnterClosed.
func (current *Turn) EndDraft(problem error, cancellation session.TurnCancelCause) (session.EventDraft, error) {
	ended, err := current.EndValue(problem, cancellation)
	if err != nil {
		return session.EventDraft{}, err
	}
	return session.NewEventDraft(session.TurnEnded, ended)
}

// EndValue describes the settled conclusion for persistence and acknowledgement.
func (current *Turn) EndValue(problem error, cancellation session.TurnCancelCause) (session.TurnEnd, error) {
	if current == nil {
		return session.TurnEnd{}, errors.New("agentloop turn: Turn is nil")
	}
	if current.currentState != StateSettling {
		return session.TurnEnd{}, current.transitionError("prepare end")
	}
	var reason session.TurnEndReason
	switch current.conclusion {
	case TurnResultCompleted:
		reason = session.TurnCompleted{}
	case TurnResultMaxTokens:
		reason = session.TurnMaxTokens{}
	case TurnResultBlocked:
		reason = session.TurnBlocked{}
	case TurnResultAborted:
		if cancellation == nil {
			cancellation = session.UserCancelCause{}
		}
		reason = session.TurnAborted{
			Reason: cancellation,
		}
	case TurnResultError:
		reason = session.TurnError{
			Error: failureFromError(problem),
		}
	default:
		return session.TurnEnd{}, errors.New("agentloop turn: terminal conclusion is required")
	}
	return session.TurnEnd{
		Turn:   current.number,
		Reason: reason,
	}, nil
}

// ResultFromStep maps a completed Step outcome to its enclosing Turn conclusion.
func ResultFromStep(result stepstate.StepResult) TurnResult {
	switch result {
	case stepstate.StepResultContinue:
		return TurnResultContinue
	case stepstate.StepResultCompleted:
		return TurnResultCompleted
	case stepstate.StepResultMaxTokens:
		return TurnResultMaxTokens
	case stepstate.StepResultAborted:
		return TurnResultAborted
	case stepstate.StepResultError:
		return TurnResultError
	default:
		return TurnResultError
	}
}

func failureFromError(problem error) llm.LlmFailure {
	var llmProblem *llm.LlmError
	if errors.As(problem, &llmProblem) {
		return llmProblem.Failure()
	}
	if problem == nil {
		return llm.LlmFailure{
			Message: "unknown Agent Loop failure",
			Code:    "UNKNOWN",
		}
	}
	return llm.LlmFailure{
		Message: problem.Error(),
		Code:    "UNKNOWN",
	}
}
