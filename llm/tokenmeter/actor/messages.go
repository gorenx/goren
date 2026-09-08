// Package meteractor exposes token measurement through GoAkt requests.
package meteractor

import (
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/llm/tokenmeter"
	"github.com/gorenx/goren/session"
)

// Measure supplies a committed log prefix belonging to exactly one Session creation.
type Measure struct {
	SessionID  session.SessionID
	CreationID string
	Events     []session.Event
	Header     *session.EpochHeader
}

// Measured returns a detached measurement at the supplied log cut.
type Measured struct {
	Value tokenmeter.Measurement
	Err   error
}

// EstimateMessage prices a message without allocating a Session replay fold.
type EstimateMessage struct {
	Message agentmessage.Message
}

// Estimated reports fixed-estimator tokens or an explicit validation failure.
type Estimated struct {
	Tokens int64
	Err    error
}

// Close releases only the named creation and rejects its subsequent measurements.
type Close struct {
	SessionID  session.SessionID
	CreationID string
}

// Completion acknowledges creation cleanup, retaining the original error type.
type Completion struct {
	Err error
}
