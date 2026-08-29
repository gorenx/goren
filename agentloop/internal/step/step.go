// Package step owns the state and boundary invariants of one Agent Step.
package step

import (
	"errors"
	"fmt"
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
	turnNumber   int64
	stepNumber   int64
	currentState State
	conclusion   StepResult
}

// New constructs a proposed Step position.
func New(turnNumber int64, stepNumber int64) (*Step, error) {
	if turnNumber <= 0 || stepNumber <= 0 {
		return nil, errors.New("agentloop step: position must be positive")
	}
	return &Step{
		turnNumber:   turnNumber,
		stepNumber:   stepNumber,
		currentState: StateProposed,
	}, nil
}

// EnterOpen records that step/start committed and moves the Step from
// proposed to open.
func (current *Step) EnterOpen() error {
	if current == nil {
		return errors.New("agentloop step: Step is nil")
	}
	if current.currentState != StateProposed {
		return current.transitionError("enter open")
	}
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
	if result == StepResultNone {
		return errors.New("agentloop step: settlement outcome is empty")
	}
	current.conclusion = result
	current.currentState = StateSettling
	return nil
}

// EnterClosed records that step/end committed and closes the Step.
func (current *Step) EnterClosed() error {
	if current == nil {
		return errors.New("agentloop step: Step is nil")
	}
	if current.currentState != StateSettling {
		return current.transitionError("enter closed")
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
