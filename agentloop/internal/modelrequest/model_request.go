// Package modelrequest owns retries and terminal state for one model request.
package modelrequest

import "github.com/gorenx/goren/llm"

// requestPhase identifies the business result currently awaited by the driver.
// It is private execution progress, not a second representation of the outcome.
type requestPhase uint8

const (
	requestUnstarted requestPhase = iota
	runningAttempt
	resolvingRecovery
	retryingClose
	requestComplete
)

// ModelRequest owns the attempt sequence and terminal outcome of one request.
type ModelRequest struct {
	phase           requestPhase
	attempts        int
	input           RequestInput
	provider        string
	policy          llm.RetryPolicy
	active          *Attempt
	cancellation    error
	failure         error
	outcome         Completed
	headerCommitted bool
}

// New constructs a proposed ModelRequest.
func New() *ModelRequest {
	return &ModelRequest{
		phase: requestUnstarted,
	}
}

// Attempts returns the number of started model attempts, including stream middleware.
func (current *ModelRequest) Attempts() int {
	if current == nil {
		return 0
	}
	return current.attempts
}
