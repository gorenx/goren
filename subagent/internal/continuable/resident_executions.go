package continuable

import (
	"context"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	sharedexecution "github.com/gorenx/goren/subagent/internal/execution"
)

type childSlot struct {
	mutex   sync.Mutex
	users   int
	current *currentExecution
}

type currentExecution struct {
	running    *sharedexecution.Execution
	terminator *executionTerminator
	slot       *childSlot
	wake       chan struct{}
}

// residentExecutions owns the per-child serialization slots and resident
// execution lifecycle. Service decides use cases; this object owns the mutable
// index, publication, settlement observation, and parent wake-up invariant.
type residentExecutions struct {
	agents      agent.Registry
	descendants agent.RuntimeDescendants
	sessions    session.LiveStore
	publisher   sharedexecution.EventPublisher
	failures    FailureReporter
	executions  *sharedexecution.Registry
	mutex       sync.Mutex
	slots       map[session.SessionID]*childSlot
}

func newResidentExecutions(dependencySet Dependencies) *residentExecutions {
	return &residentExecutions{
		agents:      dependencySet.Agents,
		descendants: dependencySet.Descendants,
		sessions:    dependencySet.Sessions,
		publisher:   dependencySet.Publisher,
		failures:    dependencySet.Failures,
		executions:  dependencySet.Executions,
		slots:       make(map[session.SessionID]*childSlot),
	}
}

func (residents *residentExecutions) publish(
	handle agent.Handle,
	parentAgent agent.Agent,
	seedBuilder string,
	slot *childSlot,
) (*currentExecution, error) {
	runID, identityErr := sharedexecution.NewRunID()
	if identityErr != nil {
		return nil, identityErr
	}
	terminator := &executionTerminator{
		owner:       residents,
		handle:      handle,
		parent:      parentAgent,
		seedBuilder: seedBuilder,
		runID:       runID,
		boundary:    handle.Subject.SessionValue().Seq(),
	}
	running, executionErr := sharedexecution.New(
		runID,
		handle.Subject.ID(),
		terminator,
	)
	if executionErr != nil {
		return nil, executionErr
	}
	current := &currentExecution{
		running:    running,
		terminator: terminator,
		slot:       slot,
		wake:       make(chan struct{}),
	}
	terminator.current = current
	slot.current = current
	if publishErr := sharedexecution.Publish(
		residents.executions,
		residents.publisher,
		sharedexecution.Entry{
			Execution: running,
			Mode:      subagent.ModeContinuable,
			Parent:    parentAgent,
			Subject:   handle.Subject,
			Closing:   handle.ClosingSignal(),
		},
		seedBuilder,
	); publishErr != nil {
		if slot.current == current {
			slot.current = nil
		}
		return nil, publishErr
	}
	return current, nil
}

func signal(current *currentExecution) {
	select {
	case <-current.wake:
	default:
		close(current.wake)
	}
	current.wake = make(chan struct{})
}

func (residents *residentExecutions) acquire(childID session.SessionID) *childSlot {
	residents.mutex.Lock()
	slot := residents.slots[childID]
	if slot == nil {
		slot = &childSlot{}
		residents.slots[childID] = slot
	}
	slot.users++
	residents.mutex.Unlock()
	return slot
}

func (residents *residentExecutions) release(
	childID session.SessionID,
	slot *childSlot,
) {
	residents.mutex.Lock()
	slot.users--
	residents.mutex.Unlock()
	residents.removeUnused(childID, slot)
}

func (residents *residentExecutions) detach(
	childID session.SessionID,
	current *currentExecution,
) {
	slot := current.slot
	slot.mutex.Lock()
	if slot.current == current {
		slot.current = nil
	}
	slot.mutex.Unlock()
	residents.removeUnused(childID, slot)
}

func (residents *residentExecutions) removeUnused(
	childID session.SessionID,
	slot *childSlot,
) {
	residents.mutex.Lock()
	slot.mutex.Lock()
	unused := slot.current == nil
	slot.mutex.Unlock()
	if residents.slots[childID] == slot && slot.users == 0 && unused {
		delete(residents.slots, childID)
	}
	residents.mutex.Unlock()
}

func (residents *residentExecutions) watch(current *currentExecution) {
	go func() {
		for {
			idleContext, cancelIdle := context.WithCancel(context.Background())
			idleResult := make(chan error, 1)
			go func() {
				idleResult <- current.terminator.handle.Subject.WhenIdle(
					idleContext,
				)
			}()
			current.slot.mutex.Lock()
			wakeSignal := current.wake
			current.slot.mutex.Unlock()
			select {
			case <-current.terminator.handle.ClosingSignal():
				cancelIdle()
				current.running.Stop(sharedexecution.StopExternal)
				return
			case <-wakeSignal:
				cancelIdle()
				continue
			case <-idleResult:
				cancelIdle()
			}
			current.slot.mutex.Lock()
			if current.slot.current != current ||
				current.running.State() != subagent.ExecutionActive {
				current.slot.mutex.Unlock()
				return
			}
			childAgent := current.terminator.handle.Subject
			settled := childAgent.StatusValue() == agent.StatusIdle &&
				!childAgent.InboxValue().HasPending() &&
				!residents.descendants.HasRuntimeDescendants(childAgent)
			if settled {
				current.running.Stop(sharedexecution.StopIdle)
				current.slot.mutex.Unlock()
				return
			}
			current.slot.mutex.Unlock()
		}
	}()
}

func (residents *residentExecutions) wakeParent(parentID session.SessionID) {
	residents.mutex.Lock()
	slot := residents.slots[parentID]
	residents.mutex.Unlock()
	if slot == nil {
		return
	}
	slot.mutex.Lock()
	if slot.current != nil {
		signal(slot.current)
	}
	slot.mutex.Unlock()
}

func (residents *residentExecutions) interrupt(childID session.SessionID) {
	residents.mutex.Lock()
	slot := residents.slots[childID]
	residents.mutex.Unlock()
	if slot == nil {
		return
	}
	slot.mutex.Lock()
	defer slot.mutex.Unlock()
	current := slot.current
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
	residents.mutex.Lock()
	slots := make([]*childSlot, 0, len(residents.slots))
	for _, slot := range residents.slots {
		slots = append(slots, slot)
	}
	residents.mutex.Unlock()
	targets := make([]*currentExecution, 0, len(slots))
	for _, slot := range slots {
		slot.mutex.Lock()
		if slot.current != nil {
			targets = append(targets, slot.current)
		}
		slot.mutex.Unlock()
	}
	for _, current := range targets {
		current.running.Stop(sharedexecution.StopModule)
	}
	for _, current := range targets {
		select {
		case <-current.terminator.handle.ClosingSignal():
		case <-ctx.Done():
			return context.Cause(ctx)
		}
	}
	return nil
}
