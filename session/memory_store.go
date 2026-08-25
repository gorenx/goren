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

type eventPublisher interface {
	Publish(context.Context, plugin.Event) error
}

type memoryStoreState uint8

const (
	memoryStoreOpen memoryStoreState = iota
	memoryStoreClosing
	memoryStoreClosed
)

// memoryStore owns live Session membership and lifecycle decisions.
// Plugin publication and event dispatch are provided by a separate adapter.
type memoryStore struct {
	mu            sync.RWMutex
	registrations map[SessionID]*registration
	order         []SessionID
	counter       uint64
	timeSource    TimeSource
	failureReport PostCommitFailureReporter
	publisher     eventPublisher
	state         memoryStoreState
	closeDone     chan struct{}
	closeErr      error
}

var _ LiveStore = (*memoryStore)(nil)

type systemTimeSource struct{}

func (systemTimeSource) CurrentTime() time.Time {
	return time.Now()
}

// newMemoryStore constructs the business Store behind the Session Plugin.
func newMemoryStore(
	options MemoryStoreOptions,
	publisher eventPublisher,
) (*memoryStore, error) {
	selectedTimeSource := options.TimeSource
	if selectedTimeSource == nil {
		selectedTimeSource = systemTimeSource{}
	}
	if options.PostCommitFailures == nil {
		return nil, errors.New("session: post-commit failure reporter is required")
	}
	if publisher == nil {
		return nil, errors.New("session: event publisher is required")
	}
	return &memoryStore{
		registrations: make(map[SessionID]*registration),
		timeSource:    selectedTimeSource,
		failureReport: options.PostCommitFailures,
		publisher:     publisher,
		state:         memoryStoreOpen,
	}, nil
}

// Create performs prepare, enter, and announce as one owned registration.
func (registry *memoryStore) Create(
	requestContext context.Context,
	identifier *SessionID,
	options CreateOptions,
) (Handle, error) {
	if requestContext == nil {
		return nil, errors.New("session: create Context is nil")
	}
	conversation, err := registry.Prepare(identifier, options)
	if err != nil {
		return nil, err
	}
	membership, err := registry.enter(conversation)
	if err != nil {
		return nil, err
	}
	if err := registry.Announce(requestContext, conversation); err != nil {
		rollbackContext := context.WithoutCancel(requestContext)
		return nil, errors.Join(err, membership.rollback(rollbackContext))
	}
	return membership, nil
}

// Prepare validates and constructs a detached unpublished Session.
func (registry *memoryStore) Prepare(identifier *SessionID, options CreateOptions) (Context, error) {
	resolved := SessionID("")
	registry.mu.Lock()
	if registry.state != memoryStoreOpen {
		registry.mu.Unlock()
		return nil, errors.New("session: Store is not accepting Session preparation")
	}
	if identifier == nil {
		for {
			registry.counter++
			resolved = SessionID(fmt.Sprintf("session-%d", registry.counter))
			if _, exists := registry.registrations[resolved]; !exists {
				break
			}
		}
	} else {
		resolved = *identifier
	}
	_, duplicate := registry.registrations[resolved]
	registry.mu.Unlock()
	if duplicate {
		return nil, fmt.Errorf("session: %q already exists", resolved)
	}
	return newContextWithClock(resolved, options, registry.timeSource)
}

// Enter publishes membership and append hooks, but does not announce creation.
func (registry *memoryStore) Enter(conversation Context) (Handle, error) {
	return registry.enter(conversation)
}

func (registry *memoryStore) enter(conversation Context) (*registration, error) {
	if conversation == nil {
		return nil, errors.New("session: cannot enter nil Session")
	}
	concrete, valid := conversation.(*coordinator)
	if !valid {
		return nil, errors.New("session: Session was not created by the session package")
	}
	current := &registration{
		store:        registry,
		conversation: concrete,
		state:        registrationEntered,
	}
	identifier := concrete.ID()
	registry.mu.Lock()
	if registry.state != memoryStoreOpen {
		registry.mu.Unlock()
		return nil, errors.New("session: Store is not accepting Session entry")
	}
	if _, exists := registry.registrations[identifier]; exists {
		registry.mu.Unlock()
		return nil, fmt.Errorf("session: %q already exists", identifier)
	}
	if !concrete.attach(current) {
		registry.mu.Unlock()
		return nil, fmt.Errorf("session: %q is already attached to a Store", identifier)
	}
	registry.registrations[identifier] = current
	registry.order = append(registry.order, identifier)
	registry.mu.Unlock()

	return current, nil
}

// Announce emits the creation edge exactly once for an entered Session.
func (registry *memoryStore) Announce(requestContext context.Context, conversation Context) error {
	if requestContext == nil {
		return errors.New("session: announce Context is nil")
	}
	entry, err := registry.liveRegistration(conversation)
	if err != nil {
		return err
	}
	return entry.announce(requestContext)
}

// Flush awaits every registered durability observer for a live Session.
func (registry *memoryStore) Flush(requestContext context.Context, conversation Context) error {
	entry, err := registry.liveRegistration(conversation)
	if err != nil {
		return err
	}
	barrier, err := entry.conversation.orderedBarrier(requestContext)
	if err != nil {
		return err
	}
	return registry.publishFlush(requestContext, conversation, barrier)
}

func (registry *memoryStore) publishFlush(
	requestContext context.Context,
	conversation Context,
	barrier WriteBarrier,
) error {
	return registry.publisher.Publish(
		requestContext,
		FlushRequested{
			Conversation: conversation,
			Barrier:      barrier,
		},
	)
}

// Get looks up one live Session.
func (registry *memoryStore) Get(identifier SessionID) (Context, bool) {
	registry.mu.RLock()
	current, found := registry.registrations[identifier]
	registry.mu.RUnlock()
	if !found || !current.isLive() {
		return nil, false
	}
	return current.conversation, true
}

// List returns live Sessions in entry order.
func (registry *memoryStore) List() []Context {
	registry.mu.RLock()
	identifiers := append([]SessionID(nil), registry.order...)
	copied := make(map[SessionID]*registration, len(registry.registrations))
	for identifier, current := range registry.registrations {
		copied[identifier] = current
	}
	registry.mu.RUnlock()

	result := make([]Context, 0, len(identifiers))
	for _, identifier := range identifiers {
		current := copied[identifier]
		if current != nil && current.isLive() {
			result = append(result, current.conversation)
		}
	}
	return result
}

// Close detaches every live Session in reverse entry order.
func (registry *memoryStore) Close(closeContext context.Context) error {
	if closeContext == nil {
		return errors.New("session: close Context is nil")
	}
	registry.mu.Lock()
	if registry.state == memoryStoreClosed {
		registry.mu.Unlock()
		return nil
	}
	if registry.state == memoryStoreClosing && registry.closeDone != nil {
		done := registry.closeDone
		registry.mu.Unlock()
		select {
		case <-done:
			registry.mu.RLock()
			closeErr := registry.closeErr
			registry.mu.RUnlock()
			return closeErr
		case <-closeContext.Done():
			return context.Cause(closeContext)
		}
	}
	registry.state = memoryStoreClosing
	done := make(chan struct{})
	registry.closeDone = done
	active := make([]*registration, 0, len(registry.order))
	for _, identifier := range registry.order {
		if current := registry.registrations[identifier]; current != nil {
			active = append(active, current)
		}
	}
	registry.mu.Unlock()

	var closeErr error
	for index := len(active) - 1; index >= 0; index-- {
		closeErr = errors.Join(closeErr, active[index].release(closeContext))
	}
	registry.mu.Lock()
	registry.closeErr = closeErr
	registry.closeDone = nil
	if closeErr == nil {
		registry.state = memoryStoreClosed
	}
	close(done)
	registry.mu.Unlock()
	return closeErr
}

func (registry *memoryStore) liveRegistration(conversation Context) (*registration, error) {
	if conversation == nil {
		return nil, errors.New("session: nil Session is not live")
	}
	identifier := conversation.ID()
	registry.mu.RLock()
	current := registry.registrations[identifier]
	registry.mu.RUnlock()
	if current == nil || current.conversation != conversation || !current.isLive() {
		return nil, fmt.Errorf("session: %q is not live in this Store", identifier)
	}
	return current, nil
}

func (registry *memoryStore) removeRegistration(current *registration) {
	identifier := current.conversation.ID()
	registry.mu.Lock()
	if registry.registrations[identifier] == current {
		delete(registry.registrations, identifier)
		registry.order = slices.DeleteFunc(registry.order, func(candidate SessionID) bool {
			return candidate == identifier
		})
	}
	registry.mu.Unlock()
}

func (registry *memoryStore) publishDisposed(requestContext context.Context, conversation Context) {
	_ = safelyDispatch(func() error {
		return registry.publisher.Publish(
			requestContext,
			Disposed{
				Conversation: conversation,
			},
		)
	})
}

func (registry *memoryStore) publishCreated(requestContext context.Context, conversation Context) error {
	return safelyDispatch(func() error {
		return registry.publisher.Publish(
			requestContext,
			Created{
				Conversation: conversation,
			},
		)
	})
}

func (registry *memoryStore) reportPostCommitFailure(failure PostCommitFailure) {
	defer func() { _ = recover() }()
	registry.failureReport.ReportPostCommitFailure(failure)
}

func safelyDispatch(operation func() error) (dispatchErr error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			dispatchErr = fmt.Errorf("session: listener panicked: %v", recovered)
		}
	}()
	return operation()
}
