package continuation

import (
	"context"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

func (owner *Manager) assertAvailable(
	requestContext context.Context,
	childID session.SessionID,
	checkPersistence bool,
) error {
	if _, found := owner.dependencies.Agents.Get(childID); found {
		return duplicateChild(childID)
	}
	if _, found := owner.dependencies.Sessions.Get(childID); found {
		return duplicateChild(childID)
	}
	if checkPersistence {
		snapshots, listErr := owner.dependencies.Persistence.ListSnapshots(requestContext)
		if listErr != nil {
			return listErr
		}
		for _, snapshot := range snapshots {
			if snapshot.Header.ID == childID {
				return duplicateChild(childID)
			}
		}
	}
	return nil
}

func (owner *Manager) assertAdmitting(parentAgent agent.Agent) error {
	if parentAgent == nil || !owner.dependencies.Agents.Contains(parentAgent) {
		return unauthorized("continuable operation requires the exact live parent Agent")
	}
	owner.activations.mutex.Lock()
	closing := owner.activations.admission == activationsClosing
	owner.activations.mutex.Unlock()
	if closing {
		return &subagent.Error{
			Code:    subagent.ErrorDraining,
			Message: "continuable subagents are closing; the operation was not admitted",
		}
	}
	return nil
}

func (owner *Manager) lockFor(childID session.SessionID) *sync.Mutex {
	owner.activations.mutex.Lock()
	defer owner.activations.mutex.Unlock()
	childMutex := owner.activations.locks[childID]
	if childMutex == nil {
		childMutex = &sync.Mutex{}
		owner.activations.locks[childID] = childMutex
	}
	return childMutex
}
