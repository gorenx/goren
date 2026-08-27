package subagents

import (
	"context"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

// implementation is the common lifecycle contract for one Subagent mode.
// Mode-specific behavior stays behind this consumer-owned boundary.
type implementation interface {
	Mode() subagent.Mode
	Interrupt(context.Context, session.SessionID) error
	Close(context.Context) error
}

// oneShot is the complete OneShot contract consumed by Service.
type oneShot interface {
	implementation
	Start(
		context.Context,
		subagent.OneShotStartCommand,
	) (subagent.Execution, error)
}

// continuable is the complete Continuable contract consumed by Service.
type continuable interface {
	implementation
	Start(
		context.Context,
		subagent.ContinuableStartCommand,
	) (subagent.Execution, error)
	Resume(
		context.Context,
		agent.Agent,
		session.SessionID,
		llm.UserMessage,
	) (llm.MessageID, error)
}

// bound is the complete Bound contract consumed by Service.
type bound interface {
	implementation
	Start(
		context.Context,
		subagent.BoundStartCommand,
	) (subagent.Execution, error)
}

type admissionState uint8

const (
	admissionInactive admissionState = iota
	admissionAccepting
	admissionClosing
	admissionClosed
)
