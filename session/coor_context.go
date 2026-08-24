package session

import (
	"context"
	"sync"

	"github.com/gorenx/goren/llm"
)

// coordinator is the single ordering and lifecycle facade for one Session.
// Consumers receive only Context; the log and queue never escape.
type coordinator struct {
	log   *log
	queue requestQueue

	lifecycleMutex sync.RWMutex
	publisher      publisher
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

func (owner *coordinator) DeriveMessages() ([]llm.Message, error) {
	if owner == nil {
		return nil, nil
	}
	return owner.log.DeriveMessages()
}

func (owner *coordinator) currentPublisher() publisher {
	owner.lifecycleMutex.RLock()
	current := owner.publisher
	owner.lifecycleMutex.RUnlock()
	return current
}

func (owner *coordinator) attach(candidate publisher) bool {
	owner.lifecycleMutex.Lock()
	defer owner.lifecycleMutex.Unlock()
	if owner.publisher != nil {
		return false
	}
	owner.publisher = candidate
	return true
}

func (owner *coordinator) detach(candidate publisher) {
	owner.lifecycleMutex.Lock()
	if owner.publisher == candidate {
		owner.publisher = nil
	}
	owner.lifecycleMutex.Unlock()
}

type publisher interface {
	publishAppend(context.Context, Event)
}
