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

// MemoryStore is the in-memory Provider for LiveStore.
type MemoryStore struct {
	plugin.Base
	mu            sync.RWMutex
	registrations map[SessionID]*registration
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
		registrations: make(map[SessionID]*registration),
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
func (registry *MemoryStore) Prepare(identifier *SessionID, options CreateOptions) (Context, error) {
	resolved := SessionID("")
	registry.mu.Lock()
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
func (registry *MemoryStore) Enter(conversation Context) (Handle, error) {
	return registry.enter(conversation)
}

func (registry *MemoryStore) enter(conversation Context) (*registration, error) {
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
		live:         true,
	}
	identifier := concrete.ID()
	registry.mu.Lock()
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
func (registry *MemoryStore) Announce(requestContext context.Context, conversation Context) error {
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
func (registry *MemoryStore) Flush(requestContext context.Context, conversation Context) error {
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

func (registry *MemoryStore) publishFlush(
	requestContext context.Context,
	conversation Context,
	barrier WriteBarrier,
) error {
	return plugin.Publish(
		requestContext,
		registry,
		FlushRequested{
			Conversation: conversation,
			Barrier:      barrier,
		},
	)
}

// Get looks up one live Session.
func (registry *MemoryStore) Get(identifier SessionID) (Context, bool) {
	registry.mu.RLock()
	current, found := registry.registrations[identifier]
	registry.mu.RUnlock()
	if !found || !current.isLive() {
		return nil, false
	}
	return current.conversation, true
}

// List returns live Sessions in entry order.
func (registry *MemoryStore) List() []Context {
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
func (registry *MemoryStore) Close(closeContext context.Context) error {
	active := registry.List()
	var closeErr error
	for index := len(active) - 1; index >= 0; index-- {
		entry, err := registry.liveRegistration(active[index])
		if err == nil {
			closeErr = errors.Join(closeErr, entry.release(closeContext))
		}
	}
	return closeErr
}

func (registry *MemoryStore) liveRegistration(conversation Context) (*registration, error) {
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

func (registry *MemoryStore) removeRegistration(current *registration) {
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

func (registry *MemoryStore) publishDisposed(requestContext context.Context, conversation Context) {
	_ = safelyDispatch(func() error {
		return plugin.Publish(
			requestContext,
			registry,
			Disposed{
				Conversation: conversation,
			},
		)
	})
}

func (registry *MemoryStore) publishCreated(requestContext context.Context, conversation Context) error {
	return safelyDispatch(func() error {
		return plugin.Publish(
			requestContext,
			registry,
			Created{
				Conversation: conversation,
			},
		)
	})
}

func (registry *MemoryStore) reportPostCommitFailure(failure PostCommitFailure) {
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
