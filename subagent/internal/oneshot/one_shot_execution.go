package oneshot

import (
	"context"
	"errors"
	"sync"

	"github.com/gorenx/goren/agent"
	agentregistry "github.com/gorenx/goren/agent/registry"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/subagent/internal/agentclient"
	sharedexecution "github.com/gorenx/goren/subagent/internal/execution"
)

type oneShotExecution struct {
	mutex          sync.RWMutex
	phase          subagent.ExecutionState
	done           chan struct{}
	terminal       subagent.Terminal
	terminalErr    error
	executions     *sharedexecution.Registry
	child          agentclient.Agent
	parent         agentregistry.Creation
	seedBuilder    string
	executionRunID subagent.RunID
	childSessionID session.SessionID
	boundary       int64
	structured     *structuredOutput
	publisher      sharedexecution.EventPublisher
}

func newOneShotExecution(runID subagent.RunID, childID session.SessionID) (*oneShotExecution, error) {
	if runID == "" || childID == "" {
		return nil, errors.New("subagent: OneShot execution identity is incomplete")
	}
	return &oneShotExecution{
		phase:          subagent.ExecutionStarting,
		done:           make(chan struct{}),
		executionRunID: runID,
		childSessionID: childID,
	}, nil
}

func (executionValue *oneShotExecution) Activate() error {
	executionValue.mutex.Lock()
	defer executionValue.mutex.Unlock()
	if executionValue.phase != subagent.ExecutionStarting {
		return errors.New("subagent: OneShot execution is no longer starting")
	}
	executionValue.phase = subagent.ExecutionActive
	return nil
}

func (executionValue *oneShotExecution) Stop(cause sharedexecution.CloseCause) {
	executionValue.stop(context.Background(), cause)
}

func (executionValue *oneShotExecution) stop(closeContext context.Context, cause sharedexecution.CloseCause) {
	executionValue.mutex.Lock()
	if executionValue.phase == subagent.ExecutionStopping || executionValue.phase == subagent.ExecutionStopped {
		executionValue.mutex.Unlock()
		return
	}
	executionValue.phase = subagent.ExecutionStopping
	executionValue.mutex.Unlock()
	go executionValue.terminate(context.WithoutCancel(closeContext), cause)
}

func (executionValue *oneShotExecution) terminate(closeContext context.Context, cause sharedexecution.CloseCause) {
	terminalValue, terminalErr := executionValue.close(closeContext, cause)
	executionValue.mutex.Lock()
	executionValue.terminal = sharedexecution.CloneTerminal(terminalValue)
	executionValue.terminalErr = terminalErr
	executionValue.phase = subagent.ExecutionStopped
	close(executionValue.done)
	executionValue.mutex.Unlock()
}

func (executionValue *oneShotExecution) close(closeContext context.Context, cause sharedexecution.CloseCause) (subagent.Terminal, error) {
	cancelled := cause != sharedexecution.CloseNormal
	var terminalErr error
	if cancelled {
		terminalErr = errors.Join(terminalErr, executionValue.child.Cancel(closeContext, agent.DisposedCancel{}, agent.CancelOptions{KeepInbox: false}))
	}
	terminalErr = errors.Join(terminalErr, executionValue.child.WhenIdle(closeContext))
	view, readErr := executionValue.child.Read(closeContext)
	terminalErr = errors.Join(terminalErr, readErr)
	terminalValue := subagent.Terminal{StopReason: subagent.StopError}
	if readErr == nil {
		terminalValue, readErr = readTerminal(view.Snapshot.Events, executionValue.boundary, cancelled, executionValue.structured)
		terminalErr = errors.Join(terminalErr, readErr)
	}
	if terminalErr != nil {
		terminalValue.StopReason = subagent.StopError
	}
	if executionValue.publisher != nil {
		executionValue.publisher.PublishEnded(executionValue.parent, subagent.Ended{
			RunID:                executionValue.executionRunID,
			Provider:             executionValue.seedBuilder,
			ID:                   executionValue.childSessionID,
			Local:                true,
			StopReason:           terminalValue.StopReason,
			LastAssistantMessage: terminalValue.Output,
		})
	}
	if executionValue.executions != nil {
		executionValue.executions.Remove(executionValue)
	}
	if cause != sharedexecution.CloseExternal {
		terminalErr = errors.Join(terminalErr, executionValue.child.Close(closeContext))
	}
	return terminalValue, terminalErr
}

func (executionValue *oneShotExecution) RunID() subagent.RunID { return executionValue.executionRunID }
func (executionValue *oneShotExecution) ChildID() session.SessionID {
	return executionValue.childSessionID
}
func (executionValue *oneShotExecution) State() subagent.ExecutionState {
	executionValue.mutex.RLock()
	defer executionValue.mutex.RUnlock()
	return executionValue.phase
}
func (executionValue *oneShotExecution) Wait(waitContext context.Context) error {
	select {
	case <-executionValue.done:
		executionValue.mutex.RLock()
		defer executionValue.mutex.RUnlock()
		return executionValue.terminalErr
	case <-waitContext.Done():
		return context.Cause(waitContext)
	}
}
func (executionValue *oneShotExecution) Result() (subagent.Terminal, bool) {
	executionValue.mutex.RLock()
	defer executionValue.mutex.RUnlock()
	if executionValue.phase != subagent.ExecutionStopped {
		return subagent.Terminal{}, false
	}
	return sharedexecution.CloneTerminal(executionValue.terminal), true
}
func (executionValue *oneShotExecution) Dispose(closeContext context.Context) error {
	return executionValue.StopAndWait(closeContext, sharedexecution.CloseDisposed)
}
func (executionValue *oneShotExecution) StopAndWait(closeContext context.Context, cause sharedexecution.CloseCause) error {
	if closeContext == nil {
		closeContext = context.Background()
	}
	executionValue.stop(closeContext, cause)
	return executionValue.Wait(closeContext)
}

var _ sharedexecution.ManagedExecution = (*oneShotExecution)(nil)
