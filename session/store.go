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

const (
	PluginName               = "@deepseek-ai/dsh-session"
	SessionCreatedEventName  = "session/created"
	SessionDisposedEventName = "session/disposed"
	SessionAppendedEventName = "session/event"
	SessionFlushEventName    = "session/flush"
)

// LiveStore owns live Session membership and publication lifecycle. Persistence is
// deliberately absent; adapters observe EventAppended and FlushRequested.
type LiveStore interface {
	plugin.Service
	Create(context.Context, *SessionID, CreateOptions) (SessionHandle, error)
	Prepare(*SessionID, CreateOptions) (*Session, error)
	Enter(*Session) (SessionHandle, error)
	Announce(context.Context, *Session) error
	Flush(context.Context, *Session) error
	Get(SessionID) (*Session, bool)
	List() []*Session
}

// SessionHandle owns one live Session membership.
type SessionHandle interface {
	Session() *Session
	Release(context.Context) error
}

// SessionCreated is the vetoable publication edge after a Session enters the Store.
type SessionCreated struct {
	Conversation *Session
}

func (SessionCreated) EventName() string { return SessionCreatedEventName }
func (SessionCreated) EventDelivery() plugin.DeliveryPolicy {
	return plugin.DeliveryOrdered
}

// SessionDisposed announces post-removal cleanup as a contained notification.
type SessionDisposed struct {
	Conversation *Session
}

func (SessionDisposed) EventName() string { return SessionDisposedEventName }
func (SessionDisposed) EventDelivery() plugin.DeliveryPolicy {
	return plugin.DeliveryBestEffort
}

// SessionEventAppended carries the exact Session Event that has already committed.
type SessionEventAppended struct {
	Conversation *Session
	Committed    Event
}

func (SessionEventAppended) EventName() string { return SessionAppendedEventName }
func (SessionEventAppended) EventDelivery() plugin.DeliveryPolicy {
	return plugin.DeliveryBestEffort
}

// SessionFlushRequested asks every durability observer to reach its checkpoint.
type SessionFlushRequested struct {
	Conversation *Session
}

func (SessionFlushRequested) EventName() string { return SessionFlushEventName }
func (SessionFlushRequested) EventDelivery() plugin.DeliveryPolicy {
	return plugin.DeliveryParallel
}

// TimeSource supplies timestamps without coupling the Store to process globals.
type TimeSource interface {
	CurrentTime() time.Time
}

// PostCommitFailure identifies deferred work that failed after an Event commit.
type PostCommitFailure struct {
	SessionID SessionID
	Error     error
}

// PostCommitFailureReporter receives failures that cannot roll back a committed Event.
type PostCommitFailureReporter interface {
	ReportPostCommitFailure(PostCommitFailure)
}

// MemoryStoreOptions supplies technical collaborators without adding persistence policy.
type MemoryStoreOptions struct {
	TimeSource         TimeSource
	PostCommitFailures PostCommitFailureReporter
}

// MemoryStore is the in-memory Provider for LiveStore.
type MemoryStore struct {
	plugin.Base
	mu            sync.RWMutex
	entries       map[SessionID]*storeEntry
	order         []SessionID
	counter       uint64
	timeSource    TimeSource
	failureReport PostCommitFailureReporter
}

type systemTimeSource struct{}

func (systemTimeSource) CurrentTime() time.Time {
	return time.Now()
}

// NewMemoryStore constructs an empty Session Service Plugin.
func NewMemoryStore(options MemoryStoreOptions) (*MemoryStore, error) {
	selectedTimeSource := options.TimeSource
	if selectedTimeSource == nil {
		selectedTimeSource = systemTimeSource{}
	}
	if options.PostCommitFailures == nil {
		return nil, errors.New("session: post-commit failure reporter is required")
	}
	return &MemoryStore{
		entries:       make(map[SessionID]*storeEntry),
		timeSource:    selectedTimeSource,
		failureReport: options.PostCommitFailures,
	}, nil
}

// Manifest declares the canonical Session Store Service.
func (owner *MemoryStore) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: PluginName,
		Provides: []plugin.ProvidedService{
			plugin.NewProvidedService[LiveStore](owner),
		},
	}
}

// Apply validates startup cancellation before Store publication.
func (*MemoryStore) Apply(requestContext context.Context) error {
	return requestContext.Err()
}

// Dispose releases every live Session in reverse entry order.
func (registry *MemoryStore) Dispose(closeContext context.Context) error {
	return registry.Close(closeContext)
}

// Create performs prepare, enter, and announce as one owned registration.
func (registry *MemoryStore) Create(
	requestContext context.Context,
	identifier *SessionID,
	options CreateOptions,
) (SessionHandle, error) {
	conversation, err := registry.Prepare(identifier, options)
	if err != nil {
		return nil, err
	}
	handle, err := registry.Enter(conversation)
	if err != nil {
		return nil, err
	}
	if err := registry.Announce(requestContext, conversation); err != nil {
		return nil, errors.Join(err, handle.Release(requestContext))
	}
	return handle, nil
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
	return newWithClock(resolved, options, registry.timeSource)
}

// Enter publishes membership and append hooks, but does not announce creation.
func (registry *MemoryStore) Enter(conversation *Session) (SessionHandle, error) {
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

	return &memorySessionHandle{
		entry: entry,
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

// Flush awaits every registered durability observer for a live Session.
func (registry *MemoryStore) Flush(requestContext context.Context, conversation *Session) error {
	if _, err := registry.liveEntry(conversation); err != nil {
		return err
	}
	return plugin.Publish(
		requestContext,
		registry,
		SessionFlushRequested{
			Conversation: conversation,
		},
	)
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
	_ = plugin.Publish(
		requestContext,
		registry,
		SessionDisposed{
			Conversation: conversation,
		},
	)
}

func (registry *MemoryStore) publishCreated(requestContext context.Context, conversation *Session) error {
	return safelyDispatch(func() error {
		return plugin.Publish(
			requestContext,
			registry,
			SessionCreated{
				Conversation: conversation,
			},
		)
	})
}

type memorySessionHandle struct {
	entry *storeEntry
}

func (handleState *memorySessionHandle) Session() *Session {
	if handleState == nil || handleState.entry == nil {
		return nil
	}
	return handleState.entry.conversation
}

func (handleState *memorySessionHandle) Release(closeContext context.Context) error {
	if handleState == nil || handleState.entry == nil {
		return nil
	}
	handleState.entry.detach(closeContext)
	return nil
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
	return func(committed Event) {
		deferred := entry.publishAppend(committed)
		entry.finishAppend()
		if err := deferred.run(); err != nil {
			entry.owner.failureReport.ReportPostCommitFailure(
				PostCommitFailure{
					SessionID: entry.conversation.ID(),
					Error:     err,
				},
			)
		}
	}, nil
}

func (entry *storeEntry) publishAppend(committed Event) *afterEventQueue {
	deferred := &afterEventQueue{}
	_ = plugin.Publish(
		deferred.context(),
		entry.owner,
		SessionEventAppended{
			Conversation: entry.conversation,
			Committed:    cloneEvent(committed),
		},
	)
	return deferred
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
