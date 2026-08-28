package session

import (
	"context"
	"errors"
	"fmt"

	"github.com/gorenx/goren/plugin"
)

type releaseAttempt struct {
	done           chan struct{}
	requestContext context.Context
	err            error
}

type sessionHandle struct {
	conversation *coordinator
}

func (handleState *sessionHandle) Session() Context {
	if handleState == nil {
		return nil
	}
	return handleState.conversation
}

func (handleState *sessionHandle) Release(closeContext context.Context) error {
	if handleState == nil || handleState.conversation == nil {
		return nil
	}
	return handleState.conversation.release(closeContext)
}

func (owner *coordinator) enter(store *memoryStore) (Handle, error) {
	if owner == nil {
		return nil, errors.New("session: enter nil Session")
	}
	if store == nil {
		return nil, errors.New("session: enter nil Store")
	}
	item := &request{
		kind:           requestEnter,
		requestContext: context.Background(),
		store:          store,
		ready:          make(chan struct{}),
	}
	if err := owner.admit(item); err != nil {
		return nil, err
	}
	<-item.ready
	err := owner.executeEnter(item)
	owner.queue.complete(item)
	if err != nil {
		return nil, err
	}
	return &sessionHandle{conversation: owner}, nil
}

func (owner *coordinator) executeEnter(item *request) error {
	if err := item.store.attachExact(owner); err != nil {
		owner.queue.mutex.Lock()
		completionErr := owner.machine.finishEnter(false)
		owner.queue.mutex.Unlock()
		return errors.Join(err, completionErr)
	}
	owner.queue.mutex.Lock()
	completionErr := owner.machine.finishEnter(true)
	if completionErr == nil {
		owner.store = item.store
	}
	owner.queue.mutex.Unlock()
	if completionErr != nil {
		item.store.removeExact(owner)
	}
	return completionErr
}

func (owner *coordinator) announce(
	requestContext context.Context,
	store *memoryStore,
) error {
	if requestContext == nil {
		return errors.New("session: announce Context is nil")
	}
	if err := contextCause(requestContext); err != nil {
		return err
	}
	if owner.isReentry(requestContext) {
		return ErrWriteReentry
	}
	item := &request{
		kind:           requestAnnounce,
		requestContext: requestContext,
		store:          store,
		ready:          make(chan struct{}),
	}
	if err := owner.admit(item); err != nil {
		return err
	}
	<-item.ready
	dispatchErr, attempt, start, err := owner.executeAnnounce(item)
	owner.queue.complete(item)
	if start {
		go owner.runRelease(attempt.request, attempt.state)
	}
	if err != nil {
		return err
	}
	if attempt.state != nil {
		releaseErr := waitForRelease(context.WithoutCancel(requestContext), attempt.state)
		return errors.Join(dispatchErr, releaseErr)
	}
	return dispatchErr
}

type scheduledRelease struct {
	request *request
	state   *releaseAttempt
}

func (owner *coordinator) executeAnnounce(
	item *request,
) (error, scheduledRelease, bool, error) {
	owner.queue.mutex.Lock()
	if owner.store != item.store {
		owner.queue.mutex.Unlock()
		return nil, scheduledRelease{}, false, errors.New("session: Announce Store does not own Session")
	}
	if err := owner.machine.startAnnounce(); err != nil {
		owner.queue.mutex.Unlock()
		return nil, scheduledRelease{}, false, err
	}
	owner.queue.mutex.Unlock()

	eventContext := owner.publicationContext(item.requestContext)
	dispatchErr := item.store.publishCreated(eventContext, owner)

	owner.queue.mutex.Lock()
	if err := owner.machine.finishAnnounce(); err != nil {
		owner.queue.mutex.Unlock()
		return dispatchErr, scheduledRelease{}, false, err
	}
	if dispatchErr == nil {
		owner.queue.mutex.Unlock()
		return nil, scheduledRelease{}, false, nil
	}
	scheduled, start, err := owner.scheduleReleaseLocked(
		context.WithoutCancel(item.requestContext),
	)
	owner.queue.mutex.Unlock()
	return dispatchErr, scheduled, start, err
}

func (owner *coordinator) flush(
	requestContext context.Context,
	store *memoryStore,
) error {
	if requestContext == nil {
		return errors.New("session: flush Context is nil")
	}
	if err := contextCause(requestContext); err != nil {
		return err
	}
	if owner.isReentry(requestContext) {
		return ErrWriteReentry
	}
	item := &request{
		kind:           requestFlush,
		requestContext: requestContext,
		store:          store,
		ready:          make(chan struct{}),
	}
	if err := owner.admit(item); err != nil {
		return err
	}
	<-item.ready
	err := owner.executeFlush(item)
	owner.queue.complete(item)
	return err
}

func (owner *coordinator) executeFlush(item *request) error {
	if err := contextCause(item.requestContext); err != nil {
		return err
	}
	owner.queue.mutex.Lock()
	store := owner.store
	owner.queue.mutex.Unlock()
	if store == nil || store != item.store {
		return errors.New("session: Flush Store does not own Session")
	}
	return store.publishFlush(
		owner.publicationContext(item.requestContext),
		owner,
		owner.log.currentBarrier(),
	)
}

func (owner *coordinator) release(closeContext context.Context) error {
	if closeContext == nil {
		return errors.New("session: release Context is nil")
	}
	if err := contextCause(closeContext); err != nil {
		return err
	}
	if owner.isReentry(closeContext) {
		return ErrWriteReentry
	}
	owner.queue.mutex.Lock()
	scheduled, start, err := owner.scheduleReleaseLocked(context.WithoutCancel(closeContext))
	owner.queue.mutex.Unlock()
	if err != nil {
		return err
	}
	if scheduled.state == nil {
		return nil
	}
	if start {
		go owner.runRelease(scheduled.request, scheduled.state)
	}
	return waitForRelease(closeContext, scheduled.state)
}

func (owner *coordinator) scheduleReleaseLocked(
	ownedContext context.Context,
) (scheduledRelease, bool, error) {
	if owner.terminalAttempt != nil {
		return scheduledRelease{state: owner.terminalAttempt}, false, nil
	}
	if owner.machine.isClosed() {
		return scheduledRelease{}, false, nil
	}
	started, err := owner.machine.requestRelease()
	if err != nil {
		return scheduledRelease{}, false, err
	}
	if !started {
		return scheduledRelease{}, false, nil
	}
	attempt := &releaseAttempt{
		done:           make(chan struct{}),
		requestContext: ownedContext,
	}
	item := &request{
		kind:           requestRelease,
		requestContext: ownedContext,
		ready:          make(chan struct{}),
	}
	owner.terminalAttempt = attempt
	owner.queue.appendLocked(item)
	return scheduledRelease{
		request: item,
		state:   attempt,
	}, true, nil
}

func (owner *coordinator) runRelease(item *request, attempt *releaseAttempt) {
	<-item.ready
	disposalNotification, releaseErr := owner.executeRelease(attempt.requestContext)

	owner.queue.mutex.Lock()
	completionErr := owner.machine.finishRelease(releaseErr == nil)
	owner.queue.mutex.Unlock()
	releaseErr = errors.Join(releaseErr, completionErr)

	if releaseErr == nil && disposalNotification {
		owner.queue.mutex.Lock()
		store := owner.store
		owner.queue.mutex.Unlock()
		if store != nil {
			store.publishDisposed(owner.publicationContext(attempt.requestContext), owner)
		}
	}

	owner.queue.mutex.Lock()
	attempt.err = releaseErr
	owner.terminalAttempt = nil
	owner.queue.completeLocked(item)
	close(attempt.done)
	owner.queue.mutex.Unlock()
}

func (owner *coordinator) executeRelease(
	ownedContext context.Context,
) (bool, error) {
	owner.queue.mutex.Lock()
	if err := owner.machine.startRelease(); err != nil {
		owner.queue.mutex.Unlock()
		return false, err
	}
	store := owner.store
	disposalNotification := owner.machine.shouldPublishDisposed()
	owner.queue.mutex.Unlock()
	if store == nil {
		return false, errors.New("session: attached Session lost its Store")
	}
	err := store.publishFlush(
		owner.publicationContext(ownedContext),
		owner,
		owner.log.currentBarrier(),
	)
	if err != nil &&
		!errors.Is(err, plugin.ErrPluginNotActive) &&
		!errors.Is(err, plugin.ErrPluginNotBound) {
		return false, err
	}
	if !store.removeExact(owner) {
		return false, fmt.Errorf("session: %q lost its exact Store membership", owner.ID())
	}
	return disposalNotification, nil
}

func waitForRelease(waitContext context.Context, attempt *releaseAttempt) error {
	select {
	case <-attempt.done:
		return attempt.err
	case <-waitContext.Done():
		return context.Cause(waitContext)
	}
}

func (owner *coordinator) isReentry(requestContext context.Context) bool {
	active, found := requestContext.Value(reentryKey{}).(*coordinator)
	return found && active == owner
}

func (owner *coordinator) publicationContext(requestContext context.Context) context.Context {
	return context.WithValue(requestContext, reentryKey{}, owner)
}
