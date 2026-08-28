package bound

import (
	"sync"
	"sync/atomic"

	"github.com/gorenx/goren/session"
)

type bindingKey struct {
	parentID session.SessionID
	childID  session.SessionID
}

// bindingSlot serializes config replacement, resident replacement, and
// message admission for one exact parent-child binding.
type bindingSlot struct {
	mutex   sync.Mutex
	current atomic.Pointer[currentExecution]
}

func (slot *bindingSlot) loadCurrent() *currentExecution {
	return slot.current.Load()
}

func (slot *bindingSlot) storeCurrent(current *currentExecution) {
	slot.current.Store(current)
}

func (slot *bindingSlot) clearCurrent(expected *currentExecution) {
	slot.current.CompareAndSwap(expected, nil)
}
