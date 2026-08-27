package bound

import (
	"sync"

	"github.com/gorenx/goren/session"
)

type operationKey struct {
	parentID session.SessionID
	childID  session.SessionID
}

type operation struct {
	mutex        sync.Mutex
	currentMutex sync.Mutex
	current      *currentExecution
}

func (owner *operation) loadCurrent() *currentExecution {
	owner.currentMutex.Lock()
	defer owner.currentMutex.Unlock()
	return owner.current
}

func (owner *operation) storeCurrent(current *currentExecution) {
	owner.currentMutex.Lock()
	owner.current = current
	owner.currentMutex.Unlock()
}

func (owner *operation) clearCurrent(expected *currentExecution) {
	owner.currentMutex.Lock()
	if owner.current == expected {
		owner.current = nil
	}
	owner.currentMutex.Unlock()
}
