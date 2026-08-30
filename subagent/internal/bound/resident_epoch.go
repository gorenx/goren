package bound

import (
	"context"
	"errors"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	sharedexecution "github.com/gorenx/goren/subagent/internal/execution"
)

// residentEpoch owns the lifecycle of one Bound child Agent materialized from
// one Definition revision. It does not retain its boundChild owner.
type residentEpoch struct {
	mutex              sync.RWMutex
	phase              subagent.ExecutionState
	done               chan struct{}
	terminal           subagent.Terminal
	terminalErr        error
	executionRunID     subagent.RunID
	childSessionID     session.SessionID
	handle             agent.Handle
	definitionRevision int64
	provider           string
	sessions           session.LiveStore
	failures           FailureReporter
	publisher          sharedexecution.EventPublisher
	parent             agent.Agent
	executions         *sharedexecution.Registry
}

func newResidentEpoch(
	executionRunID subagent.RunID,
	childSessionID session.SessionID,
) (*residentEpoch, error) {
	if executionRunID == "" || childSessionID == "" {
		return nil, errors.New("subagent: Bound execution identity is incomplete")
	}
	return &residentEpoch{
		phase:          subagent.ExecutionStarting,
		done:           make(chan struct{}),
		executionRunID: executionRunID,
		childSessionID: childSessionID,
	}, nil
}

func (resident *residentEpoch) Activate() error {
	resident.mutex.Lock()
	defer resident.mutex.Unlock()
	if resident.phase != subagent.ExecutionStarting {
		return errors.New("subagent: Bound execution is no longer starting")
	}
	resident.phase = subagent.ExecutionActive
	return nil
}

func (resident *residentEpoch) Stop(cause sharedexecution.CloseCause) {
	resident.stop(context.Background(), cause)
}

func (resident *residentEpoch) stop(
	closeContext context.Context,
	cause sharedexecution.CloseCause,
) {
	resident.mutex.Lock()
	if resident.phase == subagent.ExecutionStopping ||
		resident.phase == subagent.ExecutionStopped {
		resident.mutex.Unlock()
		return
	}
	resident.phase = subagent.ExecutionStopping
	resident.mutex.Unlock()
	go resident.terminate(context.WithoutCancel(closeContext), cause)
}

func (resident *residentEpoch) terminate(
	closeContext context.Context,
	cause sharedexecution.CloseCause,
) {
	terminalValue, terminalErr := resident.close(closeContext, cause)
	resident.mutex.Lock()
	resident.terminal = sharedexecution.CloneTerminal(terminalValue)
	resident.terminalErr = terminalErr
	resident.phase = subagent.ExecutionStopped
	close(resident.done)
	resident.mutex.Unlock()
}

func (resident *residentEpoch) followup(
	requestContext context.Context,
	messageValue agentmessage.UserMessage,
) error {
	if err := resident.handle.Subject.Followup(messageValue); err != nil {
		return err
	}
	return resident.sessions.Flush(
		requestContext,
		resident.handle.Subject.SessionValue(),
	)
}

// close settles this exact resident epoch and releases its Agent Handle.
func (resident *residentEpoch) close(
	closeContext context.Context,
	cause sharedexecution.CloseCause,
) (subagent.Terminal, error) {
	if cause != sharedexecution.CloseNormal {
		resident.handle.Subject.Cancel(
			agent.ParentCancel{},
			agent.CancelOptions{
				KeepInbox: false,
			},
		)
	}
	var terminalErr error
	if err := resident.handle.Subject.WhenIdle(closeContext); err != nil {
		terminalErr = errors.Join(terminalErr, err)
	}
	if resident.sessions != nil {
		if err := resident.sessions.Flush(
			context.WithoutCancel(closeContext),
			resident.handle.Subject.SessionValue(),
		); err != nil && resident.failures != nil {
			resident.failures.ReportBoundFinalFlushFailure(
				FinalFlushFailure{
					ChildID: resident.handle.Subject.ID(),
					Error:   err,
				},
			)
		}
	}
	stopReason := subagent.StopAborted
	if cause == sharedexecution.CloseNormal {
		stopReason = subagent.StopCompleted
	}
	if terminalErr != nil {
		stopReason = subagent.StopError
	}
	terminalValue := subagent.Terminal{
		StopReason: stopReason,
	}
	if resident.publisher != nil {
		resident.publisher.PublishEnded(
			resident.parent,
			subagent.Ended{
				RunID:      resident.executionRunID,
				Provider:   resident.provider,
				ID:         resident.childSessionID,
				Local:      true,
				StopReason: stopReason,
			},
		)
	}
	resident.executions.Remove(resident)
	if cause != sharedexecution.CloseExternal {
		terminalErr = errors.Join(
			terminalErr,
			resident.handle.Dispose(context.WithoutCancel(closeContext)),
		)
	}
	return terminalValue, terminalErr
}

func (resident *residentEpoch) RunID() subagent.RunID {
	return resident.executionRunID
}

func (resident *residentEpoch) ChildID() session.SessionID {
	return resident.childSessionID
}

func (resident *residentEpoch) State() subagent.ExecutionState {
	resident.mutex.RLock()
	stateValue := resident.phase
	resident.mutex.RUnlock()
	return stateValue
}

func (resident *residentEpoch) Wait(waitContext context.Context) error {
	if waitContext == nil {
		return errors.New("subagent: Bound Wait context is nil")
	}
	select {
	case <-resident.done:
		resident.mutex.RLock()
		terminalErr := resident.terminalErr
		resident.mutex.RUnlock()
		return terminalErr
	case <-waitContext.Done():
		return context.Cause(waitContext)
	}
}

func (resident *residentEpoch) Result() (subagent.Terminal, bool) {
	resident.mutex.RLock()
	defer resident.mutex.RUnlock()
	if resident.phase != subagent.ExecutionStopped {
		return subagent.Terminal{}, false
	}
	return sharedexecution.CloneTerminal(resident.terminal), true
}

func (resident *residentEpoch) Dispose(closeContext context.Context) error {
	return resident.StopAndWait(closeContext, sharedexecution.CloseDisposed)
}

func (resident *residentEpoch) StopAndWait(
	closeContext context.Context,
	cause sharedexecution.CloseCause,
) error {
	if closeContext == nil {
		closeContext = context.Background()
	}
	resident.stop(closeContext, cause)
	return resident.Wait(closeContext)
}

var _ sharedexecution.ManagedExecution = (*residentEpoch)(nil)
