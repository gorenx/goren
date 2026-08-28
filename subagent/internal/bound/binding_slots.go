package bound

import (
	"sync"

	"github.com/gorenx/goren/session"
)

// bindingSlots owns the synchronization identity and resident reference for
// each exact parent-child binding used at runtime.
type bindingSlots struct {
	mutex    sync.Mutex
	children map[bindingKey]*bindingSlot
}

func newBindingSlots() *bindingSlots {
	return &bindingSlots{
		children: make(map[bindingKey]*bindingSlot),
	}
}

func (slots *bindingSlots) child(
	parentID session.SessionID,
	childID session.SessionID,
) *bindingSlot {
	slots.mutex.Lock()
	defer slots.mutex.Unlock()
	key := bindingKey{
		parentID: parentID,
		childID:  childID,
	}
	current := slots.children[key]
	if current == nil {
		current = &bindingSlot{}
		slots.children[key] = current
	}
	return current
}

func (slots *bindingSlots) list() []*bindingSlot {
	slots.mutex.Lock()
	defer slots.mutex.Unlock()
	result := make([]*bindingSlot, 0, len(slots.children))
	for _, current := range slots.children {
		result = append(result, current)
	}
	return result
}

func (slots *bindingSlots) findCurrent(
	childID session.SessionID,
) *currentExecution {
	slots.mutex.Lock()
	candidates := make([]*bindingSlot, 0, len(slots.children))
	for key, current := range slots.children {
		if key.childID == childID {
			candidates = append(candidates, current)
		}
	}
	slots.mutex.Unlock()
	for _, candidate := range candidates {
		if current := candidate.loadCurrent(); current != nil {
			return current
		}
	}
	return nil
}
