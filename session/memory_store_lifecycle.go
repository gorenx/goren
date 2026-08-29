package session

import (
	"context"
	"errors"
	"fmt"

	"github.com/gorenx/goren/plugin"
)

type sessionHandle struct {
	store *memoryStore
	entry *liveEntry
}

func (handleState *sessionHandle) Session() Context {
	if handleState == nil || handleState.entry == nil {
		return nil
	}
	return handleState.entry.conversation
}

func (handleState *sessionHandle) Release(closeContext context.Context) error {
	if handleState == nil || handleState.store == nil || handleState.entry == nil {
		return nil
	}
	return handleState.store.release(closeContext, handleState.entry)
}

func (store *memoryStore) enter(conversation *sessionContext) (Handle, error) {
	if err := conversation.beginEnter(); err != nil {
		return nil, err
	}
	entry := &liveEntry{
		conversation: conversation,
		token:        &membershipToken{},
	}
	attachErr := store.attachExact(entry)
	completionErr := conversation.finishEnter(
		entry.token,
		store.publisher,
		attachErr == nil,
	)
	if attachErr == nil && completionErr != nil {
		store.removeExact(entry)
	}
	if err := errors.Join(attachErr, completionErr); err != nil {
		return nil, err
	}
	return &sessionHandle{
		store: store,
		entry: entry,
	}, nil
}

func (store *memoryStore) announce(
	requestContext context.Context,
	entry *liveEntry,
) error {
	if requestContext == nil {
		return errors.New("session: announce Context is nil")
	}
	if err := contextCause(requestContext); err != nil {
		return err
	}
	conversation := entry.conversation
	if conversation.isReentry(requestContext) {
		return ErrWriteReentry
	}
	publisher, startErr := conversation.beginAnnounce(entry.token)
	if startErr != nil {
		return startErr
	}
	dispatchErr := publisher.Created(
		conversation.publicationContext(requestContext),
		conversation,
	)
	completionErr := conversation.finishAnnounce(entry.token)
	if completionErr != nil {
		return errors.Join(dispatchErr, completionErr)
	}
	if dispatchErr == nil {
		return nil
	}
	releaseErr := store.release(context.WithoutCancel(requestContext), entry)
	return errors.Join(dispatchErr, releaseErr)
}

func (store *memoryStore) flush(
	requestContext context.Context,
	entry *liveEntry,
) error {
	if requestContext == nil {
		return errors.New("session: flush Context is nil")
	}
	if err := contextCause(requestContext); err != nil {
		return err
	}
	conversation := entry.conversation
	if conversation.isReentry(requestContext) {
		return ErrWriteReentry
	}
	publisher, err := conversation.beginFlush(entry.token)
	if err != nil {
		return err
	}
	eventErr := publisher.Flush(
		conversation.publicationContext(requestContext),
		conversation,
		conversation.writeBarrier(),
	)
	conversation.finishFlush()
	return eventErr
}

func (store *memoryStore) release(
	closeContext context.Context,
	entry *liveEntry,
) error {
	if closeContext == nil {
		return errors.New("session: release Context is nil")
	}
	if err := contextCause(closeContext); err != nil {
		return err
	}
	conversation := entry.conversation
	if conversation.isReentry(closeContext) {
		return ErrWriteReentry
	}
	attempt, err := conversation.requestRelease(
		entry.token,
		context.WithoutCancel(closeContext),
	)
	if err != nil {
		return err
	}
	if attempt == nil {
		return nil
	}
	attempt.launch.Do(func() {
		go store.runRelease(entry, attempt)
	})
	return waitForRelease(closeContext, attempt)
}

func (store *memoryStore) runRelease(
	entry *liveEntry,
	attempt *releaseAttempt,
) {
	conversation := entry.conversation
	decision, releaseErr := conversation.startRelease(entry.token, attempt)
	if releaseErr == nil {
		releaseErr = decision.publisher.Flush(
			conversation.publicationContext(attempt.requestContext),
			conversation,
			conversation.writeBarrier(),
		)
		if errors.Is(releaseErr, plugin.ErrPluginNotActive) ||
			errors.Is(releaseErr, plugin.ErrPluginNotBound) {
			releaseErr = nil
		}
	}
	if releaseErr == nil && !store.removeExact(entry) {
		releaseErr = fmt.Errorf(
			"session: %q lost its exact Store membership",
			conversation.ID(),
		)
	}
	if releaseErr == nil && decision.creation == creationAnnounced {
		decision.publisher.Disposed(
			conversation.publicationContext(attempt.requestContext),
			conversation,
		)
	}
	conversation.completeRelease(
		attempt,
		releaseErr == nil,
		releaseErr,
	)
}
