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

// memoryStore owns only the process-local exact Session index, entry order,
// and Store lifecycle. Per-Session lifecycle state belongs to lifecycleMachine.
type memoryStore struct {
	mu            sync.RWMutex
	sessions      map[SessionID]*coordinator
	order         []*coordinator
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
		sessions:      make(map[SessionID]*coordinator),
		timeSource:    selectedTimeSource,
		failureReport: options.PostCommitFailures,
		publisher:     publisher,
		state:         memoryStoreOpen,
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

func (store *memoryStore) Prepare(identifier *SessionID, options CreateOptions) (Context, error) {
	resolved := SessionID("")
	store.mu.Lock()
	if store.state != memoryStoreOpen {
		store.mu.Unlock()
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
	store.mu.Unlock()
	if duplicate {
		return nil, fmt.Errorf("session: %q already exists", resolved)
	}
	return newContextWithClock(resolved, options, store.timeSource)
}

func (store *memoryStore) Enter(conversation Context) (Handle, error) {
	if conversation == nil {
		return nil, errors.New("session: cannot enter nil Session")
	}
	owner, valid := conversation.(*coordinator)
	if !valid {
		return nil, errors.New("session: Session was not created by the session package")
	}
	return owner.enter(store)
}

func (store *memoryStore) Announce(
	requestContext context.Context,
	conversation Context,
) error {
	owner, err := store.exactCoordinator(conversation)
	if err != nil {
		return err
	}
	return owner.announce(requestContext, store)
}

func (store *memoryStore) Flush(
	requestContext context.Context,
	conversation Context,
) error {
	owner, err := store.exactCoordinator(conversation)
	if err != nil {
		return err
	}
	return owner.flush(requestContext, store)
}

func (store *memoryStore) Get(identifier SessionID) (Context, bool) {
	store.mu.RLock()
	owner := store.sessions[identifier]
	store.mu.RUnlock()
	if owner == nil || !owner.visible() {
		return nil, false
	}
	return owner, true
}

func (store *memoryStore) List() []Context {
	store.mu.RLock()
	ordered := append([]*coordinator(nil), store.order...)
	store.mu.RUnlock()
	result := make([]Context, 0, len(ordered))
	for _, owner := range ordered {
		if owner.visible() {
			result = append(result, owner)
		}
	}
	return result
}

func (store *memoryStore) Close(closeContext context.Context) error {
	if closeContext == nil {
		return errors.New("session: close Context is nil")
	}
	store.mu.Lock()
	if store.state == memoryStoreClosed {
		store.mu.Unlock()
		return nil
	}
	if store.state == memoryStoreClosing && store.closeDone != nil {
		done := store.closeDone
		store.mu.Unlock()
		select {
		case <-done:
			store.mu.RLock()
			closeErr := store.closeErr
			store.mu.RUnlock()
			return closeErr
		case <-closeContext.Done():
			return context.Cause(closeContext)
		}
	}
	store.state = memoryStoreClosing
	done := make(chan struct{})
	store.closeDone = done
	active := append([]*coordinator(nil), store.order...)
	store.mu.Unlock()

	ownedContext := context.WithoutCancel(closeContext)
	var closeErr error
	for index := len(active) - 1; index >= 0; index-- {
		closeErr = errors.Join(closeErr, active[index].release(ownedContext))
	}
	store.mu.Lock()
	store.closeErr = closeErr
	store.closeDone = nil
	if closeErr == nil {
		store.state = memoryStoreClosed
	}
	close(done)
	store.mu.Unlock()
	return closeErr
}

func (store *memoryStore) attachExact(owner *coordinator) error {
	identifier := owner.ID()
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.state != memoryStoreOpen {
		return errors.New("session: Store is not accepting Session entry")
	}
	if _, exists := store.sessions[identifier]; exists {
		return fmt.Errorf("session: %q already exists", identifier)
	}
	store.sessions[identifier] = owner
	store.order = append(store.order, owner)
	return nil
}

func (store *memoryStore) removeExact(owner *coordinator) bool {
	identifier := owner.ID()
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.sessions[identifier] != owner {
		return false
	}
	delete(store.sessions, identifier)
	store.order = slices.DeleteFunc(store.order, func(candidate *coordinator) bool {
		return candidate == owner
	})
	return true
}

func (store *memoryStore) exactCoordinator(conversation Context) (*coordinator, error) {
	if conversation == nil {
		return nil, errors.New("session: nil Session is not attached to this Store")
	}
	owner, valid := conversation.(*coordinator)
	if !valid {
		return nil, errors.New("session: Session was not created by the session package")
	}
	identifier := owner.ID()
	store.mu.RLock()
	current := store.sessions[identifier]
	store.mu.RUnlock()
	if current != owner {
		return nil, fmt.Errorf("session: %q is not attached to this Store", identifier)
	}
	return owner, nil
}

func (store *memoryStore) publishAppend(
	requestContext context.Context,
	conversation Context,
	committed Event,
) {
	publishErr := safelyDispatch(func() error {
		return store.publisher.Publish(
			requestContext,
			EventAppended{
				Conversation: conversation,
				Committed:    cloneEvent(committed),
			},
		)
	})
	if publishErr != nil {
		store.reportPostCommitFailure(
			PostCommitFailure{
				SessionID: conversation.ID(),
				Error:     publishErr,
			},
		)
	}
}

func (store *memoryStore) publishFlush(
	requestContext context.Context,
	conversation Context,
	barrier WriteBarrier,
) error {
	return store.publisher.Publish(
		requestContext,
		FlushRequested{
			Conversation: conversation,
			Barrier:      barrier,
		},
	)
}

func (store *memoryStore) publishDisposed(
	requestContext context.Context,
	conversation Context,
) {
	_ = safelyDispatch(func() error {
		return store.publisher.Publish(
			requestContext,
			Disposed{Conversation: conversation},
		)
	})
}

func (store *memoryStore) publishCreated(
	requestContext context.Context,
	conversation Context,
) error {
	return safelyDispatch(func() error {
		return store.publisher.Publish(
			requestContext,
			Created{Conversation: conversation},
		)
	})
}

func (store *memoryStore) reportPostCommitFailure(failure PostCommitFailure) {
	defer func() { _ = recover() }()
	store.failureReport.ReportPostCommitFailure(failure)
}

func safelyDispatch(operation func() error) (dispatchErr error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			dispatchErr = fmt.Errorf("session: listener panicked: %v", recovered)
		}
	}()
	return operation()
}
