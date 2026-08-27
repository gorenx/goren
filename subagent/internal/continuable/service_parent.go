package continuable

import "github.com/gorenx/goren/session"

func (owner *Service) wakeParent(parentID session.SessionID) {
	owner.mutex.Lock()
	slot := owner.slots[parentID]
	owner.mutex.Unlock()
	if slot == nil {
		return
	}
	slot.mutex.Lock()
	if slot.current != nil {
		signal(slot.current)
	}
	slot.mutex.Unlock()
}
