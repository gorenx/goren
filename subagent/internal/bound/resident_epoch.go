package bound

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/subagent"
	sharedexecution "github.com/gorenx/goren/subagent/internal/execution"
)

// residentEpoch is one published Bound child Agent epoch and its common
// Execution under one applied config revision.
type residentEpoch struct {
	owner          *boundChild
	handle         agent.Handle
	execution      *sharedexecution.Execution
	configRevision int64
	seedBuilder    string
}

// Terminate settles this exact resident epoch and releases its Agent Handle.
func (resident *residentEpoch) Terminate(
	ctx context.Context,
	cause sharedexecution.StopCause,
) (subagent.Terminal, error) {
	if cause != sharedexecution.StopNormal &&
		cause != sharedexecution.StopExternal {
		resident.handle.Subject.Cancel(
			agent.ParentCancel{},
			agent.CancelOptions{
				KeepInbox: false,
			},
		)
	}
	var terminalErr error
	if err := resident.handle.Subject.WhenIdle(ctx); err != nil {
		terminalErr = errors.Join(terminalErr, err)
	}
	if resident.owner.sessions != nil {
		if err := resident.owner.sessions.Flush(
			context.WithoutCancel(ctx),
			resident.handle.Subject.SessionValue(),
		); err != nil && resident.owner.failures != nil {
			resident.owner.failures.ReportBoundFinalFlushFailure(
				FinalFlushFailure{
					ChildID: resident.handle.Subject.ID(),
					Error:   err,
				},
			)
		}
	}
	stopReason := subagent.StopAborted
	if cause == sharedexecution.StopNormal {
		stopReason = subagent.StopCompleted
	}
	if terminalErr != nil {
		stopReason = subagent.StopError
	}
	terminalValue := subagent.Terminal{
		StopReason: stopReason,
	}
	if resident.owner.publisher != nil {
		resident.owner.publisher.PublishEnded(
			resident.owner.parent,
			subagent.Ended{
				RunID:      resident.execution.RunID(),
				Provider:   resident.seedBuilder,
				ID:         resident.handle.Subject.ID(),
				Local:      true,
				StopReason: stopReason,
			},
		)
	}
	resident.owner.executions.Remove(resident.execution)
	resident.owner.executionClosed(resident)
	if cause != sharedexecution.StopExternal {
		terminalErr = errors.Join(
			terminalErr,
			resident.handle.Dispose(context.WithoutCancel(ctx)),
		)
	}
	return terminalValue, terminalErr
}
