package session

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// sessionPhase is the exclusive attachment and publication phase of one Session.
type sessionPhase uint8

const (
	// sessionDetached has not entered a live Store.
	sessionDetached sessionPhase = iota
	// sessionEntered owns exact Store membership but has not published Created.
	sessionEntered
	// sessionAnnouncing owns the active Created publication operation.
	sessionAnnouncing
	// sessionLive has published Created and accepts normal live operations.
	sessionLive
	// sessionReleasing owns the active terminal Flush and removal operation.
	sessionReleasing
	// sessionSealed retained membership after a failed Release attempt.
	sessionSealed
	// sessionClosed has completed Release and no longer owns Store membership.
	sessionClosed
)

type membershipToken struct {
	// identity keeps tokens non-zero-sized so pointer identity remains exact.
	identity byte
}

type operationWaiter struct {
	// ready closes when this operation reaches the FIFO head.
	ready chan struct{}
}

type releaseAttempt struct {
	// launch starts the one terminal worker shared by all Release callers.
	launch sync.Once
	// done closes after the shared terminal attempt completes.
	done chan struct{}
	// requestContext outlives any individual caller waiting for Release.
	requestContext context.Context
	// waiter reserves this terminal attempt's FIFO position.
	waiter *operationWaiter
	// err is the completed result read after done closes.
	err error
}

// creationState records whether the public Created edge was completed.
type creationState uint8

const (
	// creationUnannounced suppresses Disposed because Created was not published.
	creationUnannounced creationState = iota
	// creationAnnounced requires a matching Disposed publication after removal.
	creationAnnounced
)

type releaseDecision struct {
	// publisher publishes the terminal Flush and optional Disposed edge.
	publisher eventPublisher
	// creation determines whether successful removal must publish Disposed.
	creation creationState
}

// sessionLifecycle owns lifecycle phase, operation admission order, exact
// membership, event publication eligibility, and the shared Release attempt.
// It performs no callback, Store mutation, I/O, or goroutine work.
type sessionLifecycle struct {
	mutex sync.Mutex
	// phase is the current attachment and publication lifecycle phase.
	phase sessionPhase

	// waiters contains FIFO operation signals; head identifies the active signal.
	waiters []*operationWaiter
	head    int

	// enterWaiter identifies the one reserved Store-entry operation.
	enterWaiter *operationWaiter
	// announceWaiter identifies the one reserved Created publication operation.
	announceWaiter *operationWaiter
	// creation records whether Created completed for the current membership.
	creation creationState
	// membership is the exact Store membership allowed to drive lifecycle edges.
	membership *membershipToken
	// publisher is installed with membership and removed only after Release.
	publisher eventPublisher
	// terminalAttempt is the shared Release admission and terminal-state marker.
	terminalAttempt *releaseAttempt
}

func newSessionLifecycle() sessionLifecycle {
	return sessionLifecycle{
		phase: sessionDetached,
	}
}

func (lifecycle *sessionLifecycle) beginCommit() error {
	lifecycle.mutex.Lock()
	if lifecycle.terminalAttempt != nil {
		lifecycle.mutex.Unlock()
		return ErrWritesClosed
	}
	switch lifecycle.phase {
	case sessionDetached,
		sessionEntered,
		sessionAnnouncing,
		sessionLive:
		waiter := lifecycle.appendLocked()
		lifecycle.mutex.Unlock()
		<-waiter.ready
		return nil
	default:
		lifecycle.mutex.Unlock()
		return ErrWritesClosed
	}
}

func (lifecycle *sessionLifecycle) finishCommit() {
	lifecycle.mutex.Lock()
	lifecycle.finishOperationLocked()
	lifecycle.mutex.Unlock()
}

func (lifecycle *sessionLifecycle) beginEnter() error {
	lifecycle.mutex.Lock()
	if lifecycle.terminalAttempt != nil ||
		lifecycle.phase != sessionDetached ||
		lifecycle.enterWaiter != nil {
		lifecycle.mutex.Unlock()
		return errors.New("session: Session is already attached to a Store")
	}
	waiter := lifecycle.appendLocked()
	lifecycle.enterWaiter = waiter
	lifecycle.mutex.Unlock()
	<-waiter.ready
	return nil
}

func (lifecycle *sessionLifecycle) finishEnter(
	token *membershipToken,
	publisher eventPublisher,
	succeeded bool,
) error {
	lifecycle.mutex.Lock()
	defer lifecycle.mutex.Unlock()
	if lifecycle.enterWaiter == nil ||
		lifecycle.head >= len(lifecycle.waiters) ||
		lifecycle.waiters[lifecycle.head] != lifecycle.enterWaiter ||
		lifecycle.phase != sessionDetached {
		lifecycle.finishOperationLocked()
		return errors.New("session: invalid Enter completion")
	}
	lifecycle.enterWaiter = nil
	if succeeded {
		lifecycle.phase = sessionEntered
		lifecycle.membership = token
		lifecycle.publisher = publisher
	}
	lifecycle.finishOperationLocked()
	return nil
}

func (lifecycle *sessionLifecycle) beginAnnounce(
	token *membershipToken,
) (eventPublisher, error) {
	lifecycle.mutex.Lock()
	if lifecycle.membership != token {
		lifecycle.mutex.Unlock()
		return nil, errors.New("session: Announce Store does not own Session")
	}
	if lifecycle.terminalAttempt != nil ||
		lifecycle.phase != sessionEntered ||
		lifecycle.announceWaiter != nil {
		lifecycle.mutex.Unlock()
		return nil, errors.New("session: Session cannot be announced in its current state")
	}
	waiter := lifecycle.appendLocked()
	lifecycle.announceWaiter = waiter
	lifecycle.mutex.Unlock()
	<-waiter.ready
	lifecycle.mutex.Lock()
	if lifecycle.membership != token ||
		lifecycle.announceWaiter != waiter ||
		lifecycle.head >= len(lifecycle.waiters) ||
		lifecycle.waiters[lifecycle.head] != waiter ||
		lifecycle.phase != sessionEntered {
		lifecycle.finishOperationLocked()
		lifecycle.mutex.Unlock()
		return nil, errors.New("session: invalid Announce start")
	}
	lifecycle.announceWaiter = nil
	lifecycle.phase = sessionAnnouncing
	publisher := lifecycle.publisher
	lifecycle.mutex.Unlock()
	return publisher, nil
}

func (lifecycle *sessionLifecycle) finishAnnounce(
	token *membershipToken,
) error {
	lifecycle.mutex.Lock()
	defer lifecycle.mutex.Unlock()
	if lifecycle.membership != token || lifecycle.phase != sessionAnnouncing {
		lifecycle.finishOperationLocked()
		return errors.New("session: invalid Announce completion")
	}
	lifecycle.creation = creationAnnounced
	lifecycle.phase = sessionLive
	lifecycle.finishOperationLocked()
	return nil
}

func (lifecycle *sessionLifecycle) beginFlush(
	token *membershipToken,
) (eventPublisher, error) {
	lifecycle.mutex.Lock()
	if lifecycle.membership != token {
		lifecycle.mutex.Unlock()
		return nil, errors.New("session: Flush Store does not own Session")
	}
	if lifecycle.terminalAttempt != nil {
		lifecycle.mutex.Unlock()
		return nil, ErrWritesClosed
	}
	switch lifecycle.phase {
	case sessionEntered,
		sessionAnnouncing,
		sessionLive:
		waiter := lifecycle.appendLocked()
		lifecycle.mutex.Unlock()
		<-waiter.ready
		lifecycle.mutex.Lock()
		if lifecycle.membership != token || lifecycle.publisher == nil {
			lifecycle.finishOperationLocked()
			lifecycle.mutex.Unlock()
			return nil, errors.New("session: Flush Store does not own Session")
		}
		publisher := lifecycle.publisher
		lifecycle.mutex.Unlock()
		return publisher, nil
	default:
		lifecycle.mutex.Unlock()
		return nil, ErrWritesClosed
	}
}

func (lifecycle *sessionLifecycle) finishFlush() {
	lifecycle.mutex.Lock()
	lifecycle.finishOperationLocked()
	lifecycle.mutex.Unlock()
}

func (lifecycle *sessionLifecycle) requestRelease(
	token *membershipToken,
	ownedContext context.Context,
) (*releaseAttempt, error) {
	lifecycle.mutex.Lock()
	defer lifecycle.mutex.Unlock()
	if lifecycle.terminalAttempt != nil {
		if lifecycle.membership != token {
			return nil, errors.New("session: Release Store does not own Session")
		}
		return lifecycle.terminalAttempt, nil
	}
	if lifecycle.phase == sessionClosed {
		return nil, nil
	}
	if lifecycle.membership != token {
		return nil, errors.New("session: Release Store does not own Session")
	}
	switch lifecycle.phase {
	case sessionEntered,
		sessionAnnouncing,
		sessionLive,
		sessionSealed:
	default:
		return nil, fmt.Errorf(
			"session: Session cannot be released from lifecycle state %d",
			lifecycle.phase,
		)
	}
	attempt := &releaseAttempt{
		done:           make(chan struct{}),
		requestContext: ownedContext,
		waiter:         lifecycle.appendLocked(),
	}
	lifecycle.terminalAttempt = attempt
	return attempt, nil
}

func (lifecycle *sessionLifecycle) startRelease(
	token *membershipToken,
	attempt *releaseAttempt,
) (releaseDecision, error) {
	<-attempt.waiter.ready
	lifecycle.mutex.Lock()
	defer lifecycle.mutex.Unlock()
	if lifecycle.membership != token ||
		lifecycle.terminalAttempt != attempt {
		return releaseDecision{}, errors.New("session: Release started without terminal admission")
	}
	switch lifecycle.phase {
	case sessionEntered,
		sessionLive,
		sessionSealed:
		lifecycle.phase = sessionReleasing
		return releaseDecision{
			publisher: lifecycle.publisher,
			creation:  lifecycle.creation,
		}, nil
	default:
		return releaseDecision{}, fmt.Errorf(
			"session: invalid Release start from lifecycle state %d",
			lifecycle.phase,
		)
	}
}

func (lifecycle *sessionLifecycle) completeRelease(
	attempt *releaseAttempt,
	succeeded bool,
	releaseErr error,
) error {
	lifecycle.mutex.Lock()
	defer lifecycle.mutex.Unlock()
	completionErr := error(nil)
	if lifecycle.phase != sessionReleasing ||
		lifecycle.terminalAttempt != attempt ||
		lifecycle.head >= len(lifecycle.waiters) ||
		lifecycle.waiters[lifecycle.head] != attempt.waiter {
		completionErr = errors.New("session: invalid Release completion")
	} else {
		lifecycle.terminalAttempt = nil
		if succeeded {
			lifecycle.phase = sessionClosed
			lifecycle.membership = nil
			lifecycle.publisher = nil
		} else {
			lifecycle.phase = sessionSealed
		}
	}
	resultErr := errors.Join(releaseErr, completionErr)
	attempt.err = resultErr
	lifecycle.finishOperationLocked()
	close(attempt.done)
	return resultErr
}

func (lifecycle *sessionLifecycle) appendPublisher() eventPublisher {
	lifecycle.mutex.Lock()
	defer lifecycle.mutex.Unlock()
	switch lifecycle.phase {
	case sessionEntered,
		sessionAnnouncing,
		sessionLive:
		return lifecycle.publisher
	default:
		return nil
	}
}

func (lifecycle *sessionLifecycle) visible() bool {
	lifecycle.mutex.Lock()
	defer lifecycle.mutex.Unlock()
	if lifecycle.terminalAttempt != nil {
		return false
	}
	return lifecycle.phase == sessionEntered ||
		lifecycle.phase == sessionAnnouncing ||
		lifecycle.phase == sessionLive
}

func (lifecycle *sessionLifecycle) terminalAdmission() bool {
	lifecycle.mutex.Lock()
	defer lifecycle.mutex.Unlock()
	return lifecycle.terminalAttempt != nil
}

func (lifecycle *sessionLifecycle) pendingOperations() int {
	lifecycle.mutex.Lock()
	defer lifecycle.mutex.Unlock()
	pending := len(lifecycle.waiters) - lifecycle.head
	if pending > 0 {
		pending--
	}
	return pending
}

func (lifecycle *sessionLifecycle) currentPhase() sessionPhase {
	lifecycle.mutex.Lock()
	defer lifecycle.mutex.Unlock()
	return lifecycle.phase
}

func (lifecycle *sessionLifecycle) appendLocked() *operationWaiter {
	waiter := &operationWaiter{
		ready: make(chan struct{}),
	}
	wake := lifecycle.head == len(lifecycle.waiters)
	lifecycle.waiters = append(lifecycle.waiters, waiter)
	if wake {
		close(waiter.ready)
	}
	return waiter
}

func (lifecycle *sessionLifecycle) finishOperationLocked() {
	if lifecycle.head >= len(lifecycle.waiters) {
		panic("session: lifecycle has no active operation")
	}
	lifecycle.waiters[lifecycle.head] = nil
	lifecycle.head++
	if lifecycle.head < len(lifecycle.waiters) {
		close(lifecycle.waiters[lifecycle.head].ready)
		return
	}
	lifecycle.waiters = nil
	lifecycle.head = 0
}

func waitForRelease(waitContext context.Context, attempt *releaseAttempt) error {
	select {
	case <-attempt.done:
		return attempt.err
	case <-waitContext.Done():
		return context.Cause(waitContext)
	}
}
