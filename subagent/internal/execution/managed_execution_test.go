package execution

import (
	"context"
	"errors"
	"sync"

	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

type managedExecutionRecord struct {
	mutex          sync.RWMutex
	executionRunID subagent.RunID
	childSessionID session.SessionID
	phase          subagent.ExecutionState
}

func newManagedExecutionRecord(
	executionRunID subagent.RunID,
	childSessionID session.SessionID,
) *managedExecutionRecord {
	return &managedExecutionRecord{
		executionRunID: executionRunID,
		childSessionID: childSessionID,
		phase:          subagent.ExecutionStarting,
	}
}

func (record *managedExecutionRecord) Activate() error {
	record.mutex.Lock()
	defer record.mutex.Unlock()
	if record.phase != subagent.ExecutionStarting {
		return errors.New("test execution is no longer starting")
	}
	record.phase = subagent.ExecutionActive
	return nil
}

func (record *managedExecutionRecord) Stop(CloseCause) {
	record.mutex.Lock()
	record.phase = subagent.ExecutionStopped
	record.mutex.Unlock()
}

func (record *managedExecutionRecord) StopAndWait(
	_ context.Context,
	cause CloseCause,
) error {
	record.Stop(cause)
	return nil
}

func (record *managedExecutionRecord) RunID() subagent.RunID {
	return record.executionRunID
}

func (record *managedExecutionRecord) ChildID() session.SessionID {
	return record.childSessionID
}

func (record *managedExecutionRecord) State() subagent.ExecutionState {
	record.mutex.RLock()
	stateValue := record.phase
	record.mutex.RUnlock()
	return stateValue
}

func (*managedExecutionRecord) Wait(context.Context) error {
	return nil
}

func (*managedExecutionRecord) Result() (subagent.Terminal, bool) {
	return subagent.Terminal{}, false
}

func (record *managedExecutionRecord) Dispose(
	closeContext context.Context,
) error {
	return record.StopAndWait(closeContext, CloseDisposed)
}

var _ ManagedExecution = (*managedExecutionRecord)(nil)
