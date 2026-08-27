package session

import (
	"sync"

	"github.com/gorenx/goren/agentmessage"
)

// coordinator is the single ordering and lifecycle facade for one Session.
// Consumers receive only Context; the log and queue never escape.
type coordinator struct {
	log   *log
	queue requestQueue

	lifecycleMutex sync.RWMutex
	membership     *registration
}

func newCoordinator(sessionLog *log) *coordinator {
	return &coordinator{
		log: sessionLog,
	}
}

func (owner *coordinator) Header() Header {
	if owner == nil {
		return Header{}
	}
	return owner.log.Header()
}

func (owner *coordinator) ID() SessionID {
	return owner.Header().ID
}

func (owner *coordinator) FirstLiveSeq() int64 {
	if owner == nil {
		return 0
	}
	return owner.log.FirstLiveSeq()
}

func (owner *coordinator) Seq() int64 {
	if owner == nil {
		return 0
	}
	return owner.log.Seq()
}

func (owner *coordinator) Events() []Event {
	if owner == nil {
		return nil
	}
	return owner.log.Events()
}

func (owner *coordinator) Surface() Surface {
	if owner == nil {
		return Surface{}
	}
	return owner.log.Surface()
}

func (owner *coordinator) Snapshot() Snapshot {
	if owner == nil {
		return Snapshot{}
	}
	return owner.log.Snapshot()
}

func (owner *coordinator) DeriveMessages() ([]agentmessage.Message, error) {
	if owner == nil {
		return nil, nil
	}
	return owner.log.DeriveMessages()
}

func (owner *coordinator) currentMembership() *registration {
	owner.lifecycleMutex.RLock()
	current := owner.membership
	owner.lifecycleMutex.RUnlock()
	return current
}

func (owner *coordinator) attach(candidate *registration) bool {
	owner.lifecycleMutex.Lock()
	defer owner.lifecycleMutex.Unlock()
	if owner.membership != nil {
		return false
	}
	owner.membership = candidate
	return true
}

func (owner *coordinator) detach(candidate *registration) {
	owner.lifecycleMutex.Lock()
	if owner.membership == candidate {
		owner.membership = nil
	}
	owner.lifecycleMutex.Unlock()
}
