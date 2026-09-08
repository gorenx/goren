// Package step owns the state and boundary invariants of one Agent Step.
package step

import (
	"errors"
	"fmt"
	"github.com/gorenx/goren/session"
)

// State is the complete lifecycle of one admitted Step.
type State uint8

const (
	// StateProposed has not committed step/start.
	StateProposed State = iota
	// StateOpen owns a committed Step before model execution begins.
	StateOpen
	// StateRequesting owns one ModelRequest.
	StateRequesting
	// StateTooling owns one ToolBatch produced by the accepted response.
	StateTooling
	// StateSettling closes the committed Step boundary.
	StateSettling
	// StateClosed has committed step/end.
	StateClosed
)

// StepResult classifies the effect a closed Step has on its Turn.
type StepResult uint8

const (
	// StepResultNone records that no Step conclusion has been accepted.
	StepResultNone StepResult = iota
	// StepResultContinue requires a successor Step for Tool continuation.
	StepResultContinue
	// StepResultCompleted records normal model completion.
	StepResultCompleted
	// StepResultMaxTokens records a model token-limit conclusion.
	StepResultMaxTokens
	// StepResultError records terminal non-cancellation failure.
	StepResultError
	// StepResultAborted records operation cancellation.
	StepResultAborted
)

// Step owns one Step position and its open/close boundary.
type Step struct {
	inputOffset  int
	lastInputSeq int64
	input        *Input
	turnNumber   int64
	stepNumber   int64
	currentState State
	conclusion   StepResult
}

// New constructs a proposed Step position.
func New(turnNumber int64, stepNumber int64) (*Step, error) {
	if turnNumber <= 0 || stepNumber <= 0 || turnNumber > 1<<53-1 || stepNumber > 1<<53-1 {
		return nil, errors.New("agentloop step: position must contain positive safe integers")
	}
	return &Step{
		turnNumber:   turnNumber,
		stepNumber:   stepNumber,
		currentState: StateProposed,
	}, nil
}

// ConfirmStart opens the captured Step only after its matching start commits.
// The start sequence also excludes acknowledgements from older input batches.
func (current *Step) ConfirmStart(committed session.Event) error {
	if current == nil {
		return errors.New("agentloop step: Step is nil")
	}
	if current.currentState != StateProposed || current.input == nil {
		return current.transitionError("confirm start")
	}
	draft, err := current.StartDraft()
	if err != nil {
		return err
	}
	if err := draft.CheckCommit(committed); err != nil {
		return err
	}
	current.lastInputSeq = committed.Seq
	current.currentState = StateOpen
	return nil
}

// EnterRequesting records that admitted messages committed and moves the
// Step to model execution.
func (current *Step) EnterRequesting() error {
	if current == nil {
		return errors.New("agentloop step: Step is nil")
	}
	if current.currentState != StateOpen {
		return current.transitionError("enter requesting")
	}
	if current.input == nil || current.inputOffset != len(current.input.Messages) {
		return errors.New("agentloop step: admitted input has not fully committed")
	}
	current.currentState = StateRequesting
	return nil
}

// EnterTooling records that the accepted Assistant message contains Tool
// calls and moves the Step to Tool execution.
func (current *Step) EnterTooling() error {
	if current == nil {
		return errors.New("agentloop step: Step is nil")
	}
	if current.currentState != StateRequesting {
		return current.transitionError("enter tooling")
	}
	current.currentState = StateTooling
	return nil
}

// EnterSettling records the Step result and moves the Step to its
// settlement boundary before step/end commits.
func (current *Step) EnterSettling(result StepResult) error {
	if current == nil {
		return errors.New("agentloop step: Step is nil")
	}
	if current.currentState != StateOpen && current.currentState != StateRequesting &&
		current.currentState != StateTooling {
		return current.transitionError("enter settling")
	}
	switch result {
	case StepResultContinue, StepResultCompleted, StepResultMaxTokens, StepResultError, StepResultAborted:
	default:
		return fmt.Errorf("agentloop step: invalid settlement outcome %d", result)
	}
	current.conclusion = result
	current.currentState = StateSettling
	return nil
}

// ConfirmEnd closes the Step only after its matching end event commits.
func (current *Step) ConfirmEnd(committed session.Event) error {
	if current == nil {
		return errors.New("agentloop step: Step is nil")
	}
	draft, err := current.EndDraft()
	if err != nil {
		return err
	}
	if err := draft.CheckCommit(committed); err != nil {
		return err
	}
	if committed.Seq <= current.lastInputSeq {
		return errors.New("agentloop step: end precedes its start or committed input")
	}
	current.currentState = StateClosed
	return nil
}

// Position returns this Step's Turn and Step numbers.
func (current *Step) Position() (int64, int64) {
	if current == nil {
		return 0, 0
	}
	return current.turnNumber, current.stepNumber
}

// ResultValue returns the accepted Step result.
func (current *Step) ResultValue() StepResult {
	if current == nil {
		return StepResultNone
	}
	return current.conclusion
}

// StateValue returns the current Step state.
func (current *Step) StateValue() State {
	if current == nil {
		return StateClosed
	}
	return current.currentState
}

func (current *Step) transitionError(action string) error {
	return fmt.Errorf(
		"agentloop step: cannot %s from state %d",
		action,
		current.currentState,
	)
}
