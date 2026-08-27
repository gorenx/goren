package continuable

import (
	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/subagent"
	sharedexecution "github.com/gorenx/goren/subagent/internal/execution"
)

type currentExecution struct {
	running    *sharedexecution.Execution
	terminator *executionTerminator
	slot       *childSlot
	wake       chan struct{}
}

func (owner *Service) publish(
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
		owner:       owner,
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
		owner.dependencies.Executions,
		owner.dependencies.Publisher,
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
