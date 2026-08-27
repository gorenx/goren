package bound

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/subagent"
	sharedexecution "github.com/gorenx/goren/subagent/internal/execution"
)

type executionTerminator struct {
	owner       *Service
	current     *currentExecution
	handle      agent.Handle
	parent      agent.Agent
	seedBuilder string
	runID       subagent.RunID
}

func (terminator *executionTerminator) Terminate(
	ctx context.Context,
	cause sharedexecution.StopCause,
) (subagent.Terminal, error) {
	if cause != sharedexecution.StopNormal &&
		cause != sharedexecution.StopExternal {
		terminator.handle.Subject.Cancel(
			agent.ParentCancel{},
			agent.CancelOptions{
				KeepInbox: false,
			},
		)
	}
	var terminalErr error
	if err := terminator.handle.Subject.WhenIdle(ctx); err != nil {
		terminalErr = errors.Join(terminalErr, err)
	}
	if terminator.owner.dependencies.Sessions != nil {
		if err := terminator.owner.dependencies.Sessions.Flush(
			context.WithoutCancel(ctx),
			terminator.handle.Subject.SessionValue(),
		); err != nil && terminator.owner.dependencies.Failures != nil {
			terminator.owner.dependencies.Failures.ReportBoundFinalFlushFailure(
				FinalFlushFailure{
					ChildID: terminator.handle.Subject.ID(),
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
	if terminator.owner.dependencies.Publisher != nil {
		terminator.owner.dependencies.Publisher.PublishEnded(
			terminator.parent,
			subagent.Ended{
				RunID:      terminator.runID,
				Provider:   terminator.seedBuilder,
				ID:         terminator.handle.Subject.ID(),
				Local:      true,
				StopReason: stopReason,
			},
		)
	}
	terminator.owner.dependencies.Executions.Remove(
		terminator.current.running,
	)
	terminator.current.operation.clearCurrent(terminator.current)
	if cause != sharedexecution.StopExternal {
		terminalErr = errors.Join(
			terminalErr,
			terminator.handle.Dispose(context.WithoutCancel(ctx)),
		)
	}
	return terminalValue, terminalErr
}
