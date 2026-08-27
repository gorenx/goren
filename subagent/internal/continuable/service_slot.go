package continuable

import (
	"sync"

	"github.com/gorenx/goren/session"
)

type childSlot struct {
	mutex   sync.Mutex
	users   int
	current *currentExecution
}

func (owner *Service) acquireSlot(childID session.SessionID) *childSlot {
	owner.mutex.Lock()
	slot := owner.slots[childID]
	if slot == nil {
		slot = &childSlot{}
		owner.slots[childID] = slot
	}
	slot.users++
	owner.mutex.Unlock()
	return slot
}

func (owner *Service) releaseSlot(
	childID session.SessionID,
	slot *childSlot,
) {
	owner.mutex.Lock()
	slot.users--
	owner.mutex.Unlock()
	owner.removeUnusedSlot(childID, slot)
}

func (owner *Service) detach(
	childID session.SessionID,
	current *currentExecution,
) {
	slot := current.slot
	slot.mutex.Lock()
	if slot.current == current {
		slot.current = nil
	}
	slot.mutex.Unlock()
	owner.removeUnusedSlot(childID, slot)
}

func (owner *Service) removeUnusedSlot(
	childID session.SessionID,
	slot *childSlot,
) {
	owner.mutex.Lock()
	slot.mutex.Lock()
	unused := slot.current == nil
	slot.mutex.Unlock()
	if owner.slots[childID] == slot && slot.users == 0 && unused {
		delete(owner.slots, childID)
	}
	owner.mutex.Unlock()
}
