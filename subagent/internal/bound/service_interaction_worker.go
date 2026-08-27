package bound

import (
	"context"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
	subagentprojection "github.com/gorenx/goren/subagent/internal/projection"
)

func (owner *Service) ensureInteractionWorker(
	parentAgent agent.Agent,
	binding subagentprojection.BoundBinding,
	childSession session.Context,
	currentOperation *operation,
) {
	key := operationKey{
		parentID: parentAgent.ID(),
		childID:  binding.ChildSessionID,
	}
	owner.mutex.Lock()
	if owner.closing {
		owner.mutex.Unlock()
		return
	}
	parentWorkers := owner.workers[key.parentID]
	current := parentWorkers[key.childID]
	if current != nil && agent.Same(current.parent, parentAgent) {
		owner.mutex.Unlock()
		current.Notify()
		return
	}
	worker := newInteractionWorker(
		owner,
		parentAgent,
		binding,
		childSession,
		currentOperation,
	)
	if parentWorkers == nil {
		parentWorkers = make(map[session.SessionID]*interactionWorker)
		owner.workers[key.parentID] = parentWorkers
	}
	parentWorkers[key.childID] = worker
	owner.mutex.Unlock()
	if current != nil {
		current.stopOnce.Do(current.cancel)
	}
	go worker.Run()
	worker.Notify()
}

// SessionEventAppended coalesces only completed parent turns into existing
// per-binding workers. It performs no Session reads or cross-Agent effects.
func (owner *Service) SessionEventAppended(fact session.EventAppended) {
	if owner == nil || fact.Conversation == nil ||
		fact.Committed.Type != session.TurnEndEventName {
		return
	}
	owner.mutex.Lock()
	parentWorkers := owner.workers[fact.Conversation.ID()]
	workers := make([]*interactionWorker, 0, len(parentWorkers))
	for _, worker := range parentWorkers {
		workers = append(workers, worker)
	}
	owner.mutex.Unlock()
	for _, worker := range workers {
		worker.Notify()
	}
}

// AgentDisposed removes workers only for the exact parent Agent epoch.
func (owner *Service) AgentDisposed(
	_ context.Context,
	subject agent.Agent,
) error {
	if owner == nil || subject == nil {
		return nil
	}
	owner.mutex.Lock()
	parentWorkers := owner.workers[subject.ID()]
	workers := make([]*interactionWorker, 0, len(parentWorkers))
	for childID, worker := range parentWorkers {
		if !agent.Same(worker.parent, subject) {
			continue
		}
		workers = append(workers, worker)
		delete(parentWorkers, childID)
	}
	if len(parentWorkers) == 0 {
		delete(owner.workers, subject.ID())
	}
	owner.mutex.Unlock()
	for _, worker := range workers {
		worker.Stop()
	}
	return nil
}

func (owner *Service) reportInteractionFailure(
	key operationKey,
	err error,
) {
	if owner.dependencies.Failures == nil || err == nil {
		return
	}
	owner.dependencies.Failures.ReportBoundInteractionFailure(
		InteractionFailure{
			ParentID: key.parentID,
			ChildID:  key.childID,
			Error:    err,
		},
	)
}
