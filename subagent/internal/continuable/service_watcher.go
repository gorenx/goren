package continuable

import (
	"context"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/subagent"
	sharedexecution "github.com/gorenx/goren/subagent/internal/execution"
)

func (owner *Service) watch(current *currentExecution) {
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
				!owner.dependencies.Descendants.HasRuntimeDescendants(childAgent)
			if settled {
				current.running.Stop(sharedexecution.StopIdle)
				current.slot.mutex.Unlock()
				return
			}
			current.slot.mutex.Unlock()
		}
	}()
}
