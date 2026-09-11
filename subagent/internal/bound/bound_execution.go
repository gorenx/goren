package bound

import (
	"context"
	"errors"
	"sync"

	"github.com/gorenx/goren/agent"
	agentregistry "github.com/gorenx/goren/agent/registry"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	boundcontract "github.com/gorenx/goren/subagent/bound"
	"github.com/gorenx/goren/subagent/internal/agentclient"
	sharedexecution "github.com/gorenx/goren/subagent/internal/execution"
)

type boundExecution struct {
	mutex      sync.RWMutex
	phase      subagent.ExecutionState
	done       chan struct{}
	err        error
	stopReason subagent.StopReason
	owner      *Service
	runID      subagent.RunID
	child      agentclient.Agent
	parent     agentregistry.Creation
	definition boundcontract.Definition
}

func newBoundExecution(owner *Service, runID subagent.RunID, child agentclient.Agent, parent agentregistry.Creation, definition boundcontract.Definition) *boundExecution {
	return &boundExecution{phase: subagent.ExecutionStarting, done: make(chan struct{}), owner: owner, runID: runID, child: child, parent: parent, definition: definition}
}
func (execution *boundExecution) Activate() error {
	execution.mutex.Lock()
	defer execution.mutex.Unlock()
	if execution.phase != subagent.ExecutionStarting {
		return errors.New("subagent: Bound execution is no longer starting")
	}
	execution.phase = subagent.ExecutionActive
	return nil
}
func (execution *boundExecution) RunID() subagent.RunID      { return execution.runID }
func (execution *boundExecution) ChildID() session.SessionID { return execution.child.ID() }
func (execution *boundExecution) State() subagent.ExecutionState {
	execution.mutex.RLock()
	defer execution.mutex.RUnlock()
	return execution.phase
}
func (execution *boundExecution) Stop(cause sharedexecution.CloseCause) {
	execution.stop(context.Background(), cause)
}
func (execution *boundExecution) stop(ctx context.Context, cause sharedexecution.CloseCause) {
	execution.mutex.Lock()
	if execution.phase == subagent.ExecutionStopping || execution.phase == subagent.ExecutionStopped {
		execution.mutex.Unlock()
		return
	}
	execution.phase = subagent.ExecutionStopping
	execution.mutex.Unlock()
	go execution.terminate(context.WithoutCancel(ctx), cause)
}
func (execution *boundExecution) terminate(ctx context.Context, cause sharedexecution.CloseCause) {
	var closeErr error
	if cause != sharedexecution.CloseNormal {
		closeErr = errors.Join(closeErr, execution.child.Cancel(ctx, agent.ParentCancel{}, agent.CancelOptions{KeepInbox: false}))
	}
	closeErr = errors.Join(closeErr, execution.child.WhenIdle(ctx))
	stop := subagent.StopAborted
	if cause == sharedexecution.CloseNormal {
		stop = subagent.StopCompleted
	}
	if closeErr != nil {
		stop = subagent.StopError
	}
	if execution.owner.dependencies.Publisher != nil {
		execution.owner.dependencies.Publisher.PublishEnded(execution.parent, subagent.Ended{RunID: execution.runID, Provider: execution.definition.Name, ID: execution.child.ID(), Local: true, StopReason: stop})
	}
	execution.owner.dependencies.Executions.Remove(execution)
	if cause != sharedexecution.CloseExternal {
		if err := execution.child.Close(ctx); err != nil {
			if execution.owner.dependencies.Failures != nil {
				execution.owner.dependencies.Failures.ReportBoundFinalFlushFailure(FinalFlushFailure{ChildID: execution.child.ID(), Error: err})
			}
			closeErr = errors.Join(closeErr, err)
		}
	}
	execution.mutex.Lock()
	execution.err = closeErr
	execution.stopReason = stop
	execution.phase = subagent.ExecutionStopped
	close(execution.done)
	execution.mutex.Unlock()
}
func (execution *boundExecution) Wait(ctx context.Context) error {
	if ctx == nil {
		return errors.New("subagent: Bound Wait context is nil")
	}
	select {
	case <-execution.done:
		execution.mutex.RLock()
		defer execution.mutex.RUnlock()
		return execution.err
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}
func (execution *boundExecution) Result() (subagent.Terminal, bool) {
	execution.mutex.RLock()
	defer execution.mutex.RUnlock()
	if execution.phase != subagent.ExecutionStopped {
		return subagent.Terminal{}, false
	}
	return subagent.Terminal{
		StopReason: execution.stopReason,
	}, true
}
func (execution *boundExecution) Dispose(ctx context.Context) error {
	return execution.StopAndWait(ctx, sharedexecution.CloseDisposed)
}
func (execution *boundExecution) StopAndWait(ctx context.Context, cause sharedexecution.CloseCause) error {
	if ctx == nil {
		ctx = context.Background()
	}
	execution.stop(ctx, cause)
	return execution.Wait(ctx)
}

var _ sharedexecution.ManagedExecution = (*boundExecution)(nil)
