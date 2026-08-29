package session

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"
)

// memoryStoreState is the admission and shutdown phase of the live Store.
type memoryStoreState uint8

const (
	// memoryStoreOpen accepts preparation and exact Session entry.
	memoryStoreOpen memoryStoreState = iota
	// memoryStoreClosing rejects new membership while existing entries release.
	memoryStoreClosing
	// memoryStoreClosed has completed the shared Store close attempt.
	memoryStoreClosed
)

// liveEntry is one exact Store membership. The token prevents a stale Handle
// from acting on another lifecycle that later reuses the same Session ID.
type liveEntry struct {
	// conversation is the exact Session object attached by this membership.
	conversation *sessionContext
	// token authorizes only this Store membership to drive lifecycle edges.
	token *membershipToken
}

// memoryStore owns the process-local live index, entry order, ID allocation,
// and Store lifecycle. Per-Session ordering remains in sessionLifecycle.
type memoryStore struct {
	mutex sync.RWMutex

	// sessions maps SessionID keys to their exact live membership values.
	sessions map[SessionID]*liveEntry
	// order contains exact live memberships in insertion order.
	order []*liveEntry
	// counter allocates process-local default Session IDs.
	counter uint64
	// timeSource supplies deterministic event timestamps.
	timeSource TimeSource
	// publisher is installed into each exact Session membership.
	publisher eventPublisher
	// state controls Store admission and close behavior.
	state memoryStoreState
	// closeDone closes after the shared Store close attempt completes.
	closeDone chan struct{}
	// closeErr is the shared Store close result read after closeDone.
	closeErr error
}

var _ LiveStore = (*memoryStore)(nil)

type systemTimeSource struct{}

func (systemTimeSource) CurrentTime() time.Time {
	return time.Now()
}

func newMemoryStore(
	temporalSource TimeSource,
	publisher eventPublisher,
) (*memoryStore, error) {
	selectedTimeSource := temporalSource
	if selectedTimeSource == nil {
		selectedTimeSource = systemTimeSource{}
	}
	if publisher == nil {
		return nil, errors.New("session: event publisher is required")
	}
	return &memoryStore{
		sessions:   make(map[SessionID]*liveEntry),
		timeSource: selectedTimeSource,
		publisher:  publisher,
		state:      memoryStoreOpen,
	}, nil
}

func (store *memoryStore) Create(
	requestContext context.Context,
	identifier *SessionID,
	options CreateOptions,
) (Handle, error) {
	if requestContext == nil {
		return nil, errors.New("session: create Context is nil")
	}
	conversation, err := store.Prepare(identifier, options)
	if err != nil {
		return nil, err
	}
	handleState, err := store.Enter(conversation)
	if err != nil {
		return nil, err
	}
	if err := store.Announce(requestContext, conversation); err != nil {
		return nil, err
	}
	return handleState, nil
}

func (store *memoryStore) Prepare(
	identifier *SessionID,
	options CreateOptions,
) (Context, error) {
	resolved := SessionID("")
	store.mutex.Lock()
	if store.state != memoryStoreOpen {
		store.mutex.Unlock()
		return nil, errors.New("session: Store is not accepting Session preparation")
	}
	if identifier == nil {
		for {
			store.counter++
			resolved = SessionID(fmt.Sprintf("session-%d", store.counter))
			if _, exists := store.sessions[resolved]; !exists {
				break
			}
		}
	} else {
		resolved = *identifier
	}
	_, duplicate := store.sessions[resolved]
	store.mutex.Unlock()
	if duplicate {
		return nil, fmt.Errorf("session: %q already exists", resolved)
	}
	return newContextWithClock(resolved, options, store.timeSource)
}

func (store *memoryStore) Enter(conversation Context) (Handle, error) {
	if conversation == nil {
		return nil, errors.New("session: cannot enter nil Session")
	}
	ownedSession, valid := conversation.(*sessionContext)
	if !valid {
		return nil, errors.New("session: Session was not created by the session package")
	}
	return store.enter(ownedSession)
}

func (store *memoryStore) Announce(
	requestContext context.Context,
	conversation Context,
) error {
	entry, err := store.exactEntry(conversation)
	if err != nil {
		return err
	}
	return store.announce(requestContext, entry)
}

func (store *memoryStore) Flush(
	requestContext context.Context,
	conversation Context,
) error {
	entry, err := store.exactEntry(conversation)
	if err != nil {
		return err
	}
	return store.flush(requestContext, entry)
}

func (store *memoryStore) Get(identifier SessionID) (Context, bool) {
	store.mutex.RLock()
	entry := store.sessions[identifier]
	store.mutex.RUnlock()
	if entry == nil || !entry.conversation.visible() {
		return nil, false
	}
	return entry.conversation, true
}

func (store *memoryStore) List() []Context {
	store.mutex.RLock()
	ordered := append([]*liveEntry(nil), store.order...)
	store.mutex.RUnlock()
	result := make([]Context, 0, len(ordered))
	for _, entry := range ordered {
		if entry.conversation.visible() {
			result = append(result, entry.conversation)
		}
	}
	return result
}

func (store *memoryStore) attachExact(entry *liveEntry) error {
	identifier := entry.conversation.ID()
	store.mutex.Lock()
	defer store.mutex.Unlock()
	if store.state != memoryStoreOpen {
		return errors.New("session: Store is not accepting Session entry")
	}
	if _, exists := store.sessions[identifier]; exists {
		return fmt.Errorf("session: %q already exists", identifier)
	}
	store.sessions[identifier] = entry
	store.order = append(store.order, entry)
	return nil
}

func (store *memoryStore) removeExact(entry *liveEntry) bool {
	identifier := entry.conversation.ID()
	store.mutex.Lock()
	defer store.mutex.Unlock()
	if store.sessions[identifier] != entry {
		return false
	}
	delete(store.sessions, identifier)
	store.order = slices.DeleteFunc(store.order, func(candidate *liveEntry) bool {
		return candidate == entry
	})
	return true
}

func (store *memoryStore) exactEntry(conversation Context) (*liveEntry, error) {
	if conversation == nil {
		return nil, errors.New("session: nil Session is not attached to this Store")
	}
	ownedSession, valid := conversation.(*sessionContext)
	if !valid {
		return nil, errors.New("session: Session was not created by the session package")
	}
	identifier := ownedSession.ID()
	store.mutex.RLock()
	entry := store.sessions[identifier]
	store.mutex.RUnlock()
	if entry == nil || entry.conversation != ownedSession {
		return nil, fmt.Errorf("session: %q is not attached to this Store", identifier)
	}
	return entry, nil
}
