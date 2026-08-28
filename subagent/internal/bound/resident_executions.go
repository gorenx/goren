package bound

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	sharedexecution "github.com/gorenx/goren/subagent/internal/execution"
)

type currentExecution struct {
	running    *sharedexecution.Execution
	terminator *executionTerminator
	slot       *bindingSlot
	revision   int64
}

// residentExecutions owns Bound execution publication, lookup, interruption,
// external-close observation, and module shutdown.
type residentExecutions struct {
	sessions   session.LiveStore
	publisher  sharedexecution.EventPublisher
	executions *sharedexecution.Registry
	failures   FailureReporter
	slots      *bindingSlots
}

func newResidentExecutions(
	dependencySet Dependencies,
	slots *bindingSlots,
) *residentExecutions {
	return &residentExecutions{
		sessions:   dependencySet.Sessions,
		publisher:  dependencySet.Publisher,
		executions: dependencySet.Executions,
		failures:   dependencySet.Failures,
		slots:      slots,
	}
}

func (residents *residentExecutions) publish(
	handle agent.Handle,
	parentAgent agent.Agent,
	seedBuilder string,
	revision int64,
	slot *bindingSlot,
) (*currentExecution, error) {
	runID, err := sharedexecution.NewRunID()
	if err != nil {
		return nil, err
	}
	terminator := &executionTerminator{
		owner:       residents,
		handle:      handle,
		parent:      parentAgent,
		seedBuilder: seedBuilder,
		runID:       runID,
	}
	running, err := sharedexecution.New(
		runID,
		handle.Subject.ID(),
		terminator,
	)
	if err != nil {
		return nil, err
	}
	current := &currentExecution{
		running:    running,
		terminator: terminator,
		slot:       slot,
		revision:   revision,
	}
	terminator.current = current
	slot.storeCurrent(current)
	if err = sharedexecution.Publish(
		residents.executions,
		residents.publisher,
		sharedexecution.Entry{
			Execution: running,
			Mode:      subagent.ModeBound,
			Parent:    parentAgent,
			Subject:   handle.Subject,
			Closing:   handle.ClosingSignal(),
		},
		seedBuilder,
	); err != nil {
		slot.clearCurrent(current)
		return nil, err
	}
	return current, nil
}

func (*residentExecutions) watch(current *currentExecution) {
	go func() {
		<-current.terminator.handle.ClosingSignal()
		current.running.Stop(sharedexecution.StopExternal)
	}()
}

func (residents *residentExecutions) interrupt(childID session.SessionID) {
	current := residents.slots.findCurrent(childID)
	if current == nil || current.running.State() != subagent.ExecutionActive {
		return
	}
	current.terminator.handle.Subject.Cancel(
		agent.ParentCancel{},
		agent.CancelOptions{
			KeepInbox: true,
		},
	)
}

func (residents *residentExecutions) close(ctx context.Context) error {
	var closeErr error
	for _, slot := range residents.slots.list() {
		current := slot.loadCurrent()
		if current == nil {
			continue
		}
		closeErr = errors.Join(
			closeErr,
			current.running.StopAndWait(ctx, sharedexecution.StopModule),
		)
	}
	return closeErr
}
