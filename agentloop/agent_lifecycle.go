package agentloop

import (
	"errors"

	"github.com/gorenx/goren/agent"
)

// lifecycle owns readiness and close admission. Execution progress belongs to
// the active business operation, not a duplicate lifecycle phase in the actor.
type lifecycle uint8

const (
	unrestored lifecycle = iota
	restored
	available
	closingUninitialized
	closingAvailable
	released
)

// ErrState reports a business operation that is not valid at the current boundary.
var ErrState = errors.New("agentloop: operation is not valid in the current state")

func (phase lifecycle) ready() bool {
	return phase == available || phase == closingAvailable
}

func (phase lifecycle) checkAdmission() error {
	switch phase {
	case available:
		return nil
	case closingUninitialized, closingAvailable, released:
		return agent.ErrClosing
	default:
		return ErrState
	}
}

func (phase *lifecycle) beginClose() {
	switch *phase {
	case unrestored, restored:
		*phase = closingUninitialized
	case available:
		*phase = closingAvailable
	}
}

func (phase lifecycle) checkRelease() error {
	switch phase {
	case closingUninitialized, closingAvailable, released:
		return nil
	default:
		return ErrState
	}
}
