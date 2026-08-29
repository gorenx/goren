package visiblecontext

import (
	"errors"
	"fmt"
	"sync"

	"github.com/gorenx/goren/session"
)

// Directory contains the exact VisibleContext registered for each live Session.
type Directory struct {
	mutex sync.RWMutex
	// entries maps an exact process-local Session to its VisibleContext. The key
	// is the session.Context object identity, not only SessionID. The value is a
	// non-owning reference removed by the exact Registration before Agent release.
	entries map[session.Context]*VisibleContext
}

// Registration is the exact membership of one VisibleContext in a Directory.
type Registration struct {
	once         sync.Once
	directory    *Directory
	conversation session.Context
	visible      *VisibleContext
}

// NewDirectory constructs an empty exact-Session directory.
func NewDirectory() *Directory {
	return &Directory{
		entries: make(map[session.Context]*VisibleContext),
	}
}

// Register adds one exact Session and VisibleContext pair.
func (owner *Directory) Register(
	conversation session.Context,
	visible *VisibleContext,
) (*Registration, error) {
	if owner == nil || conversation == nil || visible == nil {
		return nil, errors.New(
			"agentloop visible context: Directory registration is incomplete",
		)
	}
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	if _, exists := owner.entries[conversation]; exists {
		return nil, fmt.Errorf(
			"agentloop visible context: Session %q is already registered",
			conversation.ID(),
		)
	}
	owner.entries[conversation] = visible
	return &Registration{
		directory:    owner,
		conversation: conversation,
		visible:      visible,
	}, nil
}

// Observe sends one committed event to the exact Session view, if registered.
func (owner *Directory) Observe(appended session.EventAppended) {
	if owner == nil || appended.Conversation == nil {
		return
	}
	owner.mutex.RLock()
	visible := owner.entries[appended.Conversation]
	owner.mutex.RUnlock()
	if visible != nil {
		visible.Observe(appended.Committed)
	}
}

// Clear removes every remaining registration and reports the leaked count.
func (owner *Directory) Clear() int {
	if owner == nil {
		return 0
	}
	owner.mutex.Lock()
	dangling := len(owner.entries)
	owner.entries = make(map[session.Context]*VisibleContext)
	owner.mutex.Unlock()
	return dangling
}

// Release removes this exact registration without touching a replacement.
func (registered *Registration) Release() {
	if registered == nil {
		return
	}
	registered.once.Do(func() {
		owner := registered.directory
		if owner == nil || registered.conversation == nil ||
			registered.visible == nil {
			return
		}
		owner.mutex.Lock()
		if owner.entries[registered.conversation] == registered.visible {
			delete(owner.entries, registered.conversation)
		}
		owner.mutex.Unlock()
	})
}
