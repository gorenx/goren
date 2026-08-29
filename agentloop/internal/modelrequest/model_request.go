// Package modelrequest owns retries and terminal state for one model request.
package modelrequest

import (
	"errors"
	"fmt"
)

// State is the complete lifecycle of one ModelRequest.
type State uint8

const (
	// StateProposed has not dispatched an Adapter stream.
	StateProposed State = iota
	// StateStreaming owns the currently dispatched RequestAttempt.
	StateStreaming
	// StateRetryPending accepted an explicit retry decision.
	StateRetryPending
	// StateAccepted assembled a terminal Assistant response.
	StateAccepted
	// StateFailed ended without a retryable response.
	StateFailed
	// StateAborted ended because the operation Context was canceled.
	StateAborted
)

// ModelRequest owns the attempt sequence and terminal outcome of one request.
type ModelRequest struct {
	currentState State
	attempts     int
}

// New constructs a proposed ModelRequest.
func New() *ModelRequest {
	return &ModelRequest{
		currentState: StateProposed,
	}
}

// EnterStreaming starts the next Adapter stream attempt and moves the
// request to streaming.
func (current *ModelRequest) EnterStreaming() (int, error) {
	if current == nil {
		return 0, errors.New("agentloop model request: ModelRequest is nil")
	}
	if current.currentState != StateProposed &&
		current.currentState != StateRetryPending {
		return 0, current.transitionError("enter streaming")
	}
	current.attempts++
	current.currentState = StateStreaming
	return current.attempts, nil
}

// EnterRetryPending records an explicit request-error retry decision and
// moves the request to retry-pending.
func (current *ModelRequest) EnterRetryPending() error {
	if current == nil {
		return errors.New("agentloop model request: ModelRequest is nil")
	}
	if current.currentState != StateStreaming {
		return current.transitionError("enter retry pending")
	}
	current.currentState = StateRetryPending
	return nil
}

// EnterAccepted records an assembled terminal Assistant response.
func (current *ModelRequest) EnterAccepted() error {
	return current.enterTerminal(StateAccepted, "enter accepted", true)
}

// EnterFailed records a terminal request failure that will not be
// retried.
func (current *ModelRequest) EnterFailed() error {
	return current.enterTerminal(StateFailed, "enter failed", false)
}

// EnterAborted records that the operation Context canceled the request.
func (current *ModelRequest) EnterAborted() error {
	return current.enterTerminal(StateAborted, "enter aborted", false)
}

// Attempts returns the number of dispatched Adapter streams.
func (current *ModelRequest) Attempts() int {
	if current == nil {
		return 0
	}
	return current.attempts
}

// StateValue returns the current request state.
func (current *ModelRequest) StateValue() State {
	if current == nil {
		return StateFailed
	}
	return current.currentState
}

func (current *ModelRequest) enterTerminal(
	destination State,
	action string,
	requireStreaming bool,
) error {
	if current == nil {
		return errors.New("agentloop model request: ModelRequest is nil")
	}
	terminal := current.currentState == StateAccepted ||
		current.currentState == StateFailed ||
		current.currentState == StateAborted
	if terminal || (requireStreaming && current.currentState != StateStreaming) {
		return current.transitionError(action)
	}
	current.currentState = destination
	return nil
}

func (current *ModelRequest) transitionError(action string) error {
	return fmt.Errorf(
		"agentloop model request: cannot %s from state %d",
		action,
		current.currentState,
	)
}
