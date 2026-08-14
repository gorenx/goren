package session

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/gorenx/goren/plugin"
)

// Store owns live Session membership and publication lifecycle. Persistence is
// deliberately absent; adapters observe EventAppended and FlushRequested.
type Store interface {
	Create(context.Context, *plugin.Scope, *SessionID, CreateOptions) (*Session, error)
	Prepare(*SessionID, CreateOptions) (*Session, error)
	Enter(*Session) (plugin.Disposer, error)
	Announce(context.Context, *Session) error
	Flush(context.Context, *Session) (bool, error)
	Get(SessionID) (*Session, bool)
	List() []*Session
}

// StoreService is the canonical Service Definition for the source `sessions` key.
var StoreService = plugin.DefineService[Store]("sessions")

// LifecycleNotice identifies one Session entering or leaving the live Store.
type LifecycleNotice struct {
	Session *Session
}

// AppendNotice carries the exact Session Event that has already committed.
type AppendNotice struct {
	Session *Session
	Event   Event
}

var (
	createdTopic  = plugin.DefineEvent[LifecycleNotice, struct{}]("session/created", plugin.ModeSerial)
	disposedTopic = plugin.DefineEvent[LifecycleNotice, struct{}]("session/disposed", plugin.ModeEmit)
	appendedTopic = plugin.DefineEvent[AppendNotice, struct{}]("session/event", plugin.ModeEmit)
	flushTopic    = plugin.DefineEvent[LifecycleNotice, struct{}]("session/flush", plugin.ModeParallel)
)

// LifecycleListener observes creation, disposal, or flush of one Session.
type LifecycleListener func(context.Context, *Session) error

// AppendListener observes one already committed Event.
type AppendListener func(context.Context, *Session, Event) error

// OnCreated registers a scope-owned creation listener. Returning an error
// vetoes announcement and makes the caller roll back the entered Session.
func OnCreated(pluginScope *plugin.Scope, callback LifecycleListener) (plugin.Disposer, error) {
	if callback == nil {
		return nil, errors.New("session: created listener is nil")
	}
	return plugin.OnDecision(pluginScope, createdTopic,
		func(requestContext context.Context, notice LifecycleNotice) (plugin.Decision[struct{}], error) {
			return plugin.Decision[struct{}]{}, callback(requestContext, notice.Session)
		})
}

// OnDisposed registers a scope-owned post-removal observer.
func OnDisposed(pluginScope *plugin.Scope, callback LifecycleListener) (plugin.Disposer, error) {
	if callback == nil {
		return nil, errors.New("session: disposed listener is nil")
	}
	return plugin.OnNotify(pluginScope, disposedTopic, func(requestContext context.Context, notice LifecycleNotice) error {
		return callback(requestContext, notice.Session)
	})
}

// OnEvent registers a scope-owned post-commit append observer.
func OnEvent(pluginScope *plugin.Scope, callback AppendListener) (plugin.Disposer, error) {
	if callback == nil {
		return nil, errors.New("session: event listener is nil")
	}
	return plugin.OnNotify(pluginScope, appendedTopic, func(requestContext context.Context, notice AppendNotice) error {
		return callback(requestContext, notice.Session, cloneEvent(notice.Event))
	})
}

// OnFlush registers a scope-owned awaited durability listener.
func OnFlush(pluginScope *plugin.Scope, callback LifecycleListener) (plugin.Disposer, error) {
	if callback == nil {
		return nil, errors.New("session: flush listener is nil")
	}
	return plugin.OnNotify(pluginScope, flushTopic, func(requestContext context.Context, notice LifecycleNotice) error {
		return callback(requestContext, notice.Session)
	})
}

// MemoryStoreOptions supplies process dependencies without adding persistence policy.
type MemoryStoreOptions struct {
	Clock         func() time.Time
	ObserverError func(error)
}

// MemoryStore is the in-memory Provider for Store.
type MemoryStore struct {
	mu            sync.RWMutex
	entries       map[SessionID]*storeEntry
	order         []SessionID
	counter       uint64
	sourceScope   *plugin.Scope
	clock         func() time.Time
	observerError func(error)
}

// NewMemoryStore constructs an empty provider bound to the publishing plugin scope.
func NewMemoryStore(sourceScope *plugin.Scope, options MemoryStoreOptions) (*MemoryStore, error) {
	if sourceScope == nil {
		return nil, errors.New("session: MemoryStore source scope is nil")
	}
	clock := options.Clock
	if clock == nil {
		clock = time.Now
	}
	reporter := options.ObserverError
	if reporter == nil {
		reporter = func(error) {}
	}
	return &MemoryStore{
		entries: make(map[SessionID]*storeEntry), sourceScope: sourceScope,
		clock: clock, observerError: reporter,
	}, nil
}

// Create performs prepare, enter, and announce as one caller-scope effect.
func (registry *MemoryStore) Create(requestContext context.Context, ownerScope *plugin.Scope, identifier *SessionID, options CreateOptions) (*Session, error) {
	if ownerScope == nil {
		return nil, errors.New("session: create owner scope is nil")
	}
	conversation, err := registry.Prepare(identifier, options)
	if err != nil {
		return nil, err
	}
	err = ownerScope.Effect(requestContext, "sessions.create()", func(effectContext context.Context) (plugin.Disposer, error) {
		release, enterErr := registry.Enter(conversation)
		if enterErr != nil {
			return nil, enterErr
		}
		if announceErr := registry.Announce(effectContext, conversation); announceErr != nil {
			return nil, errors.Join(announceErr, release(effectContext))
		}
		return release, nil
	})
	if err != nil {
		return nil, err
	}
	return conversation, nil
}

// Prepare validates and constructs a detached unpublished Session.
func (registry *MemoryStore) Prepare(identifier *SessionID, options CreateOptions) (*Session, error) {
	resolved := SessionID("")
	registry.mu.Lock()
	if identifier == nil {
		for {
			registry.counter++
			resolved = SessionID(fmt.Sprintf("session-%d", registry.counter))
			if _, exists := registry.entries[resolved]; !exists {
				break
			}
		}
	} else {
		resolved = *identifier
	}
	_, duplicate := registry.entries[resolved]
	registry.mu.Unlock()
	if duplicate {
		return nil, fmt.Errorf("session: %q already exists", resolved)
	}
	return newWithClock(resolved, options, registry.clock)
}

// Enter publishes membership and append hooks, but does not announce creation.
func (registry *MemoryStore) Enter(conversation *Session) (plugin.Disposer, error) {
	if conversation == nil {
		return nil, errors.New("session: cannot enter nil Session")
	}
	entry := &storeEntry{owner: registry, conversation: conversation, live: true}
	conversation.mu.Lock()
	if conversation.attachment != nil && conversation.attachment.isLive() {
		conversation.mu.Unlock()
		return nil, fmt.Errorf("session: %q is already attached to a Store", conversation.header.ID)
	}
	identifier := conversation.header.ID
	registry.mu.Lock()
	if _, exists := registry.entries[identifier]; exists {
		registry.mu.Unlock()
		conversation.mu.Unlock()
		return nil, fmt.Errorf("session: %q already exists", identifier)
	}
	conversation.attachment = entry
	registry.entries[identifier] = entry
	registry.order = append(registry.order, identifier)
	registry.mu.Unlock()
	conversation.mu.Unlock()

	return func(closeContext context.Context) error {
		entry.detach(closeContext)
		return nil
	}, nil
}

// Announce emits the creation edge exactly once for an entered Session.
func (registry *MemoryStore) Announce(requestContext context.Context, conversation *Session) error {
	entry, err := registry.liveEntry(conversation)
	if err != nil {
		return err
	}
	entry.mu.Lock()
	if entry.announced || entry.announcing {
		entry.mu.Unlock()
		return fmt.Errorf("session: %q was already announced", conversation.ID())
	}
	entry.announced = true
	entry.announcing = true
	entry.mu.Unlock()

	dispatchErr := registry.publishCreated(requestContext, conversation)
	entry.finishAnnouncement(requestContext)
	return dispatchErr
}

// Flush awaits every registered durability listener for a live Session.
func (registry *MemoryStore) Flush(requestContext context.Context, conversation *Session) (bool, error) {
	if _, err := registry.liveEntry(conversation); err != nil {
		return false, err
	}
	listenerCount, err := plugin.ParallelFrom(requestContext, registry.sourceScope, flushTopic, LifecycleNotice{Session: conversation})
	return listenerCount != 0, err
}

// Get looks up one live Session.
func (registry *MemoryStore) Get(identifier SessionID) (*Session, bool) {
	registry.mu.RLock()
	entry, found := registry.entries[identifier]
	registry.mu.RUnlock()
	if !found || !entry.isLive() {
		return nil, false
	}
	return entry.conversation, true
}

// List returns live Sessions in entry order.
func (registry *MemoryStore) List() []*Session {
	registry.mu.RLock()
	identifiers := append([]SessionID(nil), registry.order...)
	entries := make(map[SessionID]*storeEntry, len(registry.entries))
	for identifier, entry := range registry.entries {
		entries[identifier] = entry
	}
	registry.mu.RUnlock()

	result := make([]*Session, 0, len(identifiers))
	for _, identifier := range identifiers {
		entry := entries[identifier]
		if entry != nil && entry.isLive() {
			result = append(result, entry.conversation)
		}
	}
	return result
}

// Close detaches every live Session in reverse entry order.
func (registry *MemoryStore) Close(closeContext context.Context) error {
	active := registry.List()
	for index := len(active) - 1; index >= 0; index-- {
		entry, err := registry.liveEntry(active[index])
		if err == nil {
			entry.detach(closeContext)
		}
	}
	return nil
}

func (registry *MemoryStore) liveEntry(conversation *Session) (*storeEntry, error) {
	if conversation == nil {
		return nil, errors.New("session: nil Session is not live")
	}
	identifier := conversation.ID()
	registry.mu.RLock()
	entry := registry.entries[identifier]
	registry.mu.RUnlock()
	if entry == nil || entry.conversation != conversation || !entry.isLive() {
		return nil, fmt.Errorf("session: %q is not live in this Store", identifier)
	}
	return entry, nil
}

func (registry *MemoryStore) removeEntry(entry *storeEntry) {
	identifier := entry.conversation.ID()
	registry.mu.Lock()
	if registry.entries[identifier] == entry {
		delete(registry.entries, identifier)
		registry.order = slices.DeleteFunc(registry.order, func(candidate SessionID) bool {
			return candidate == identifier
		})
	}
	registry.mu.Unlock()
}

func (registry *MemoryStore) publishDisposed(requestContext context.Context, conversation *Session) {
	if err := safelyDispatch(func() error {
		return plugin.EmitFrom(requestContext, registry.sourceScope, disposedTopic, LifecycleNotice{Session: conversation})
	}); err != nil {
		registry.observerError(fmt.Errorf("session %q disposed observer: %w", conversation.ID(), err))
	}
}

func (registry *MemoryStore) publishCreated(requestContext context.Context, conversation *Session) error {
	return safelyDispatch(func() error {
		_, err := plugin.SerialFrom(requestContext, registry.sourceScope, createdTopic, LifecycleNotice{Session: conversation})
		return err
	})
}

type storeEntry struct {
	mu              sync.Mutex
	owner           *MemoryStore
	conversation    *Session
	live            bool
	announced       bool
	announcing      bool
	appending       bool
	detachRequested bool
}

func (entry *storeEntry) isLive() bool {
	entry.mu.Lock()
	defer entry.mu.Unlock()
	return entry.live
}

func (entry *storeEntry) beginAppend() (func(Event), error) {
	entry.mu.Lock()
	if !entry.live {
		entry.mu.Unlock()
		return nil, nil
	}
	if entry.appending {
		entry.mu.Unlock()
		return nil, errors.New("session: append cannot reenter during event publication")
	}
	entry.appending = true
	entry.mu.Unlock()
	captured, err := plugin.CaptureEmitFrom(entry.owner.sourceScope, appendedTopic)
	if err != nil {
		entry.finishAppend()
		return nil, err
	}
	return func(committed Event) {
		defer entry.finishAppend()
		entry.publishAppend(captured, committed)
	}, nil
}

func (entry *storeEntry) publishAppend(captured plugin.EmitSnapshot[AppendNotice], committed Event) {
	notice := AppendNotice{Session: entry.conversation, Event: cloneEvent(committed)}
	if err := safelyDispatch(func() error {
		return captured.Dispatch(context.Background(), notice)
	}); err != nil {
		entry.owner.observerError(fmt.Errorf("session %q event observer: %w", entry.conversation.ID(), err))
	}
}

func (entry *storeEntry) finishAppend() {
	entry.mu.Lock()
	entry.appending = false
	shouldDetach := entry.detachRequested && !entry.announcing
	if shouldDetach {
		entry.live = false
		entry.detachRequested = false
	}
	announced := entry.announced
	entry.mu.Unlock()
	if shouldDetach {
		entry.owner.removeEntry(entry)
		if announced {
			entry.owner.publishDisposed(context.Background(), entry.conversation)
		}
	}
}

func (entry *storeEntry) finishAnnouncement(requestContext context.Context) {
	entry.mu.Lock()
	entry.announcing = false
	shouldDetach := entry.detachRequested && !entry.appending
	if shouldDetach {
		entry.live = false
		entry.detachRequested = false
	}
	entry.mu.Unlock()
	if shouldDetach {
		entry.owner.removeEntry(entry)
		entry.owner.publishDisposed(requestContext, entry.conversation)
	}
}

func (entry *storeEntry) detach(closeContext context.Context) {
	entry.mu.Lock()
	if !entry.live {
		entry.mu.Unlock()
		return
	}
	if entry.announcing || entry.appending {
		entry.detachRequested = true
		entry.mu.Unlock()
		return
	}
	entry.live = false
	announced := entry.announced
	entry.mu.Unlock()
	entry.owner.removeEntry(entry)
	if announced {
		entry.owner.publishDisposed(closeContext, entry.conversation)
	}
}

func safelyDispatch(operation func() error) (dispatchErr error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			dispatchErr = fmt.Errorf("session: listener panicked: %v", recovered)
		}
	}()
	return operation()
}
