// Package turn owns the state and invariants of one Agent Turn.
package turn

import (
	"errors"
	"fmt"
)

// State is the complete lifecycle of one Turn.
type State uint8

const (
	// StateProposed has not committed turn/start.
	StateProposed State = iota
	// StateOpen owns a committed Turn boundary between Steps.
	StateOpen
	// StateStepping has one opened Step.
	StateStepping
	// StateStopping is dispatching the turn-stopping extension boundary.
	StateStopping
	// StateSettling is committing turn/end and completing Flush.
	StateSettling
	// StateClosed has committed turn/end and completed its required Flush.
	StateClosed
)

// TurnResult classifies the conclusion accumulated by a Turn.
type TurnResult uint8

const (
	// TurnResultNone records that no Turn conclusion exists yet.
	TurnResultNone TurnResult = iota
	// TurnResultContinue requires another Step before the Turn can conclude.
	TurnResultContinue
	// TurnResultCompleted records normal Turn completion.
	TurnResultCompleted
	// TurnResultMaxTokens records a sticky model token-limit conclusion.
	TurnResultMaxTokens
	// TurnResultBlocked records pre-step rejection before a Step opens.
	TurnResultBlocked
	// TurnResultError records terminal non-cancellation failure.
	TurnResultError
	// TurnResultAborted records typed Agent cancellation.
	TurnResultAborted
)

// Turn owns one Turn number, Step progression, and sticky conclusion.
type Turn struct {
	number       int64
	lastStep     int64
	currentState State
	conclusion   TurnResult
}

// New constructs a proposed Turn.
func New(turnNumber int64) (*Turn, error) {
	if turnNumber <= 0 {
		return nil, errors.New("agentloop turn: number must be positive")
	}
	return &Turn{
		number:       turnNumber,
		currentState: StateProposed,
	}, nil
}

// EnterOpen records that turn/start committed and moves the Turn from
// proposed to open.
func (current *Turn) EnterOpen() error {
	if current == nil {
		return errors.New("agentloop turn: Turn is nil")
	}
	if current.currentState != StateProposed {
		return current.transitionError("enter open")
	}
	current.currentState = StateOpen
	return nil
}

// ProposedStep returns the next Step number without changing state.
func (current *Turn) ProposedStep() (int64, error) {
	if current == nil {
		return 0, errors.New("agentloop turn: Turn is nil")
	}
	if current.currentState != StateOpen {
		return 0, current.transitionError("propose Step")
	}
	return current.lastStep + 1, nil
}

// EnterStepping records that step/start committed for the proposed
// number and moves the Turn to Step execution.
func (current *Turn) EnterStepping(stepNumber int64) error {
	if current == nil {
		return errors.New("agentloop turn: Turn is nil")
	}
	if current.currentState != StateOpen || stepNumber != current.lastStep+1 {
		return current.transitionError("enter stepping")
	}
	current.lastStep = stepNumber
	current.currentState = StateStepping
	return nil
}

// EnterOpenAfterStep records one closed Step, accumulates its result,
// and moves the Turn back between Steps.
func (current *Turn) EnterOpenAfterStep(result TurnResult) error {
	if current == nil {
		return errors.New("agentloop turn: Turn is nil")
	}
	if current.currentState != StateStepping {
		return current.transitionError("enter open after Step")
	}
	if result == TurnResultNone || result == TurnResultBlocked {
		return fmt.Errorf("agentloop turn: invalid Step outcome %d", result)
	}
	if result != TurnResultContinue && current.conclusion != TurnResultMaxTokens {
		current.conclusion = result
	}
	current.currentState = StateOpen
	return nil
}

// EnterStopping moves the Turn to its turn-stopping extension boundary.
func (current *Turn) EnterStopping() error {
	if current == nil {
		return errors.New("agentloop turn: Turn is nil")
	}
	if current.currentState != StateOpen || current.conclusion == TurnResultNone ||
		current.conclusion == TurnResultContinue {
		return current.transitionError("enter stopping")
	}
	current.currentState = StateStopping
	return nil
}

// EnterOpenAfterStopping accepts new next-step input supplied at the
// stopping boundary and moves the Turn back between Steps without discarding a
// sticky Turn result.
func (current *Turn) EnterOpenAfterStopping() error {
	if current == nil {
		return errors.New("agentloop turn: Turn is nil")
	}
	if current.currentState != StateStopping {
		return current.transitionError("enter open after stopping")
	}
	current.currentState = StateOpen
	return nil
}

// EnterSettling records the final Turn result and moves the Turn to its
// settlement boundary before turn/end commits.
func (current *Turn) EnterSettling(result TurnResult) error {
	if current == nil {
		return errors.New("agentloop turn: Turn is nil")
	}
	if current.currentState != StateOpen && current.currentState != StateStopping &&
		current.currentState != StateStepping {
		return current.transitionError("enter settling")
	}
	if result == TurnResultNone || result == TurnResultContinue {
		return fmt.Errorf("agentloop turn: invalid settlement outcome %d", result)
	}
	if current.conclusion != TurnResultMaxTokens || result == TurnResultAborted ||
		result == TurnResultError {
		current.conclusion = result
	}
	current.currentState = StateSettling
	return nil
}

// EnterClosed records that turn/end committed and its Flush completed,
// then closes the Turn.
func (current *Turn) EnterClosed() error {
	if current == nil {
		return errors.New("agentloop turn: Turn is nil")
	}
	if current.currentState != StateSettling {
		return current.transitionError("enter closed")
	}
	current.currentState = StateClosed
	return nil
}

// Number returns this Turn's durable number.
func (current *Turn) Number() int64 {
	if current == nil {
		return 0
	}
	return current.number
}

// LastStep returns the most recently opened Step number.
func (current *Turn) LastStep() int64 {
	if current == nil {
		return 0
	}
	return current.lastStep
}

// ResultValue returns the currently accumulated Turn result.
func (current *Turn) ResultValue() TurnResult {
	if current == nil {
		return TurnResultNone
	}
	return current.conclusion
}

// StateValue returns the current Turn state.
func (current *Turn) StateValue() State {
	if current == nil {
		return StateClosed
	}
	return current.currentState
}

func (current *Turn) transitionError(action string) error {
	return fmt.Errorf(
		"agentloop turn: cannot %s from state %d",
		action,
		current.currentState,
	)
}
