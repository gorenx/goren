package continuable

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
	boundary    int64
}

func (terminator *executionTerminator) Terminate(
	stopContext context.Context,
	cause sharedexecution.StopCause,
) (subagent.Terminal, error) {
	if cause != sharedexecution.StopIdle && cause != sharedexecution.StopNormal {
		terminator.handle.Subject.Cancel(
			agent.ParentCancel{},
			agent.CancelOptions{
				KeepInbox: false,
			},
		)
	}
	var terminalErr error
	if idleErr := terminator.handle.Subject.WhenIdle(stopContext); idleErr != nil {
		terminalErr = errors.Join(terminalErr, idleErr)
	}
	if flushErr := terminator.owner.dependencies.Sessions.Flush(
		context.WithoutCancel(stopContext),
		terminator.handle.Subject.SessionValue(),
	); flushErr != nil {
		terminator.owner.dependencies.Failures.ReportFinalFlushFailure(
			FinalFlushFailure{
				ChildID: terminator.handle.Subject.ID(),
				Error:   flushErr,
			},
		)
	}
	fallback := subagent.StopAborted
	if cause == sharedexecution.StopIdle || cause == sharedexecution.StopNormal {
		fallback = subagent.StopCompleted
	}
	executionEvents, eventsErr := currentExecutionEvents(
		terminator.handle.Subject.SessionValue(),
		terminator.boundary,
	)
	if eventsErr != nil {
		terminalErr = errors.Join(terminalErr, eventsErr)
	}
	terminalValue := subagent.Terminal{
		StopReason: executionStopReason(executionEvents, fallback),
	}
	output, outputErr := sharedexecution.SelectAssistantOutput(executionEvents)
	if outputErr != nil {
		terminalErr = errors.Join(terminalErr, outputErr)
	} else {
		terminalValue.Output = output
	}
	if terminalErr != nil {
		terminalValue.StopReason = subagent.StopError
		terminalValue.Output = nil
	}
	terminator.notifyParent(terminalValue)
	if terminator.owner.dependencies.Publisher != nil {
		terminator.owner.dependencies.Publisher.PublishEnded(
			terminator.parent,
			subagent.Ended{
				RunID:                terminator.runID,
				Provider:             terminator.seedBuilder,
				ID:                   terminator.handle.Subject.ID(),
				Local:                true,
				StopReason:           terminalValue.StopReason,
				LastAssistantMessage: terminalValue.Output,
			},
		)
	}
	terminator.owner.dependencies.Executions.Remove(
		terminator.current.running,
	)
	if cause != sharedexecution.StopExternal {
		if disposeErr := terminator.handle.Dispose(
			context.WithoutCancel(stopContext),
		); disposeErr != nil {
			terminalErr = errors.Join(terminalErr, disposeErr)
		}
	}
	terminator.owner.detach(
		terminator.handle.Subject.ID(),
		terminator.current,
	)
	terminator.owner.wakeParent(terminator.parent.ID())
	return terminalValue, terminalErr
}

var _ sharedexecution.Terminator = (*executionTerminator)(nil)
