package execution

import (
	"context"

	"github.com/gorenx/goren/subagent"
)

// CloseCause identifies why a mode-specific execution claimed its terminal
// transaction. It contains no lifecycle state or behavior.
type CloseCause string

const (
	// CloseNormal means the mode reached its normal terminal boundary.
	CloseNormal CloseCause = "normal-result"
	// CloseIdle means a Continuable execution reached idle settlement.
	CloseIdle CloseCause = "idle-settlement"
	// CloseInterrupted means an authorized caller interrupted the execution.
	CloseInterrupted CloseCause = "interrupt"
	// CloseDisposed means the public Execution owner requested disposal.
	CloseDisposed CloseCause = "dispose"
	// CloseModule means module shutdown requested convergence.
	CloseModule CloseCause = "module-shutdown"
	// CloseExternal means Agent Registry already owns structural Handle close.
	CloseExternal CloseCause = "external-agent-close"
)

// ManagedExecution is the minimal process-local control contract retained by
// the cross-mode live index. Each mode owns its concrete state machine.
type ManagedExecution interface {
	subagent.Execution
	Activate() error
	Stop(CloseCause)
	StopAndWait(context.Context, CloseCause) error
}
