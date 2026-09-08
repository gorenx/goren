package middleware

import (
	"errors"
	"github.com/gorenx/goren/agent"
)

// TurnStoppingEffect selects the next hook or completes the serial call.
type TurnStoppingEffect interface{ turnStoppingEffect() }

// InvokeTurnStopping asks one hook to complete, including its awaited input edits.
type InvokeTurnStopping struct {
	Handler RegistrationID
	Input   agent.TurnStoppingInput
}

// TurnStopped reports the serial hook result, not the completion of the Turn.
type TurnStopped struct {
	Err error
}

func (InvokeTurnStopping) turnStoppingEffect() {}
func (TurnStopped) turnStoppingEffect()        {}

// TurnStopping runs hooks in order without around continuations.
type TurnStopping struct {
	order    []RegistrationID
	input    agent.TurnStoppingInput
	next     int
	started  bool
	finished bool
	cause    error
}

// NewTurnStopping captures a fixed validated hook selection.
func NewTurnStopping(order []RegistrationID, input agent.TurnStoppingInput) (*TurnStopping, error) {
	if input.AgentID == "" || input.Turn <= 0 || input.Turn > 1<<53-1 {
		return nil, errors.New("agent middleware: invalid Turn position")
	}
	if _, err := newChain(order); err != nil {
		return nil, err
	}
	return &TurnStopping{
		order: append([]RegistrationID(nil), order...),
		input: input,
	}, nil
}

// Start invokes the first hook or completes an empty selection.
func (call *TurnStopping) Start() (TurnStoppingEffect, error) {
	if call.started {
		return nil, errors.New("agent middleware: hooks already started")
	}
	call.started = true
	return call.advance(nil), nil
}

// Complete accepts only the current hook and stops on the first error.
func (call *TurnStopping) Complete(identity RegistrationID, failure error) (TurnStoppingEffect, error) {
	if !call.started || call.finished || call.next >= len(call.order) || call.order[call.next] != identity {
		return nil, errors.New("agent middleware: unexpected hook completion")
	}
	call.next++
	return call.advance(failure), nil
}

// Cancel returns the first cancellation cause and prevents later hooks.
// The current hook must still finish its work.
func (call *TurnStopping) Cancel(cause error) error {
	if cause != nil && call.cause == nil {
		call.cause = cause
	}
	return call.cause
}

func (call *TurnStopping) advance(failure error) TurnStoppingEffect {
	if call.cause != nil {
		failure = call.cause
	}
	if failure != nil || call.next == len(call.order) {
		call.finished = true
		return TurnStopped{
			Err: failure,
		}
	}
	return InvokeTurnStopping{
		Handler: call.order[call.next],
		Input:   call.input,
	}
}
