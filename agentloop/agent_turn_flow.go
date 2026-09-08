package agentloop

import (
	"errors"

	"github.com/gorenx/goren/agent"
	turnstate "github.com/gorenx/goren/agentloop/internal/turn"
)

// TurnOperation selects work between Step boundaries without scheduling it.
type TurnOperation interface{ turnOperation() }

// ContinueSteps requires another Step in the same Turn.
type ContinueSteps struct{}

func (ContinueSteps) turnOperation() {}

// RunTurnStopping invokes the stopping hook before rechecking committed input.
type RunTurnStopping struct {
	Input agent.TurnStoppingInput
}

func (RunTurnStopping) turnOperation() {}

// ContinueTurn chooses the next boundary after Step closure or empty admission.
// The caller preserves the preceding execution failure after committing step/end.
func (subject *ReactLoopAgent) ContinueTurn(failure error) (TurnOperation, error) {
	if subject.current == nil || subject.step != nil || subject.preparing != nil || subject.request != nil {
		return nil, errors.New("agentloop: Turn continuation requires execution boundaries to finish")
	}
	switch subject.current.StateValue() {
	case turnstate.StateSettling:
		return subject.PrepareTurnEnd(failure)
	case turnstate.StateOpen:
	default:
		return nil, errors.New("agentloop: Turn continuation requires an open or settling Turn")
	}
	if failure != nil {
		return subject.PrepareTurnEnd(failure)
	}
	stopping, err := subject.BeginStopping()
	if err != nil {
		return nil, err
	}
	if stopping {
		return RunTurnStopping{
			Input: agent.TurnStoppingInput{
				AgentID: subject.identifier,
				Turn:    subject.current.Number(),
			},
		}, nil
	}
	return ContinueSteps{}, nil
}

// CompleteStopping rechecks Inbox only after the hook has actually completed.
// Cancellation or hook failure bypasses continuation and retains the error for
// Turn settlement, even when the hook accepted additional next-step input.
func (subject *ReactLoopAgent) CompleteStopping(completed agent.TurnStoppingCompleted, cancellation error) (TurnOperation, error) {
	if subject.current == nil || subject.current.StateValue() != turnstate.StateStopping {
		return nil, errors.New("agentloop: no turn-stopping hook is pending")
	}
	if failure := errors.Join(cancellation, completed.Err); failure != nil {
		return subject.PrepareTurnEnd(failure)
	}
	resume, err := subject.FinishStopping()
	if err != nil {
		return nil, err
	}
	if resume {
		return ContinueSteps{}, nil
	}
	return subject.PrepareTurnEnd(nil)
}
