package session

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/gorenx/goren/plugin"
)

type registrationState uint8

const (
	// registrationEntered is attached to the Store but has not begun publishing
	// the Session Created edge.
	registrationEntered registrationState = iota
	// registrationAnnouncing has one Created publication in progress; Release
	// waits for that publication before deciding whether Disposed is required.
	registrationAnnouncing
	// registrationLive has completed the Created publication attempt and must
	// emit the paired Disposed edge when release succeeds.
	registrationLive
	// registrationReleasing rejects a second release owner while final write
	// sealing, flush, exact Store removal, and detachment are in progress.
	registrationReleasing
	// registrationClosed is terminal and no longer belongs to the Store.
	registrationClosed
)

// registration owns one Session membership and its publication-to-release
// state machine.
type registration struct {
	mu               sync.Mutex
	store            *memoryStore
	conversation     *coordinator
	state            registrationState
	announcementDone chan struct{}
	releaseDone      chan struct{}
}

func (owner *registration) Session() Context {
	if owner == nil {
		return nil
	}
	return owner.conversation
}

func (owner *registration) Release(closeContext context.Context) error {
	if owner == nil {
		return nil
	}
	return owner.release(closeContext)
}

func (owner *registration) isLive() bool {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	return owner.state != registrationClosed
}

func (owner *registration) announce(requestContext context.Context) error {
	owner.mu.Lock()
	if owner.state != registrationEntered {
		owner.mu.Unlock()
		return fmt.Errorf("session: %q was already announced", owner.conversation.ID())
	}
	done := make(chan struct{})
	owner.state = registrationAnnouncing
	owner.announcementDone = done
	owner.mu.Unlock()

	dispatchErr := owner.store.publishCreated(requestContext, owner.conversation)
	owner.mu.Lock()
	owner.state = registrationLive
	owner.announcementDone = nil
	close(done)
	owner.mu.Unlock()
	return dispatchErr
}

func (owner *registration) publishAppend(
	requestContext context.Context,
	committed Event,
) {
	owner.mu.Lock()
	live := owner.state != registrationClosed
	owner.mu.Unlock()
	if !live {
		return
	}
	publishErr := safelyDispatch(func() error {
		return owner.store.publisher.Publish(
			requestContext,
			EventAppended{
				Conversation: owner.conversation,
				Committed:    cloneEvent(committed),
			},
		)
	})
	if publishErr != nil {
		owner.store.reportPostCommitFailure(
			PostCommitFailure{
				SessionID: owner.conversation.ID(),
				Error:     publishErr,
			},
		)
	}
}

func (owner *registration) release(closeContext context.Context) error {
	if closeContext == nil {
		return errors.New("session: release Context is nil")
	}
	for {
		owner.mu.Lock()
		switch owner.state {
		case registrationClosed:
			owner.mu.Unlock()
			return nil
		case registrationAnnouncing:
			done := owner.announcementDone
			owner.mu.Unlock()
			if err := waitForRegistration(closeContext, done); err != nil {
				return err
			}
		case registrationReleasing:
			done := owner.releaseDone
			owner.mu.Unlock()
			if err := waitForRegistration(closeContext, done); err != nil {
				return err
			}
		case registrationEntered, registrationLive:
			previous := owner.state
			done := make(chan struct{})
			owner.state = registrationReleasing
			owner.releaseDone = done
			owner.mu.Unlock()

			releaseErr := owner.finishRelease(
				closeContext,
				previous,
			)
			owner.mu.Lock()
			if releaseErr == nil {
				owner.state = registrationClosed
			} else {
				owner.state = previous
			}
			owner.releaseDone = nil
			close(done)
			owner.mu.Unlock()
			return releaseErr
		default:
			owner.mu.Unlock()
			return errors.New("session: invalid registration state")
		}
	}
}

func waitForRegistration(
	waitContext context.Context,
	done <-chan struct{},
) error {
	select {
	case <-done:
		return nil
	case <-waitContext.Done():
		return context.Cause(waitContext)
	}
}

func (owner *registration) finishRelease(
	closeContext context.Context,
	previous registrationState,
) error {
	barrier, err := owner.conversation.sealWrites(closeContext)
	if err != nil {
		return err
	}
	if err = owner.store.publishFlush(
		closeContext,
		owner.conversation,
		barrier,
	); err != nil {
		if !errors.Is(err, plugin.ErrPluginNotActive) &&
			!errors.Is(err, plugin.ErrPluginNotBound) {
			return err
		}
	}
	owner.detach(closeContext, previous)
	return nil
}

func (owner *registration) rollback(closeContext context.Context) error {
	releaseErr := owner.release(closeContext)
	if releaseErr != nil {
		owner.forceDetach(closeContext)
	}
	return releaseErr
}

func (owner *registration) forceDetach(closeContext context.Context) {
	owner.mu.Lock()
	if owner.state == registrationClosed {
		owner.mu.Unlock()
		return
	}
	previous := owner.state
	owner.state = registrationClosed
	owner.mu.Unlock()
	owner.detach(closeContext, previous)
}

func (owner *registration) detach(
	closeContext context.Context,
	previous registrationState,
) {
	owner.store.removeRegistration(owner)
	owner.conversation.detach(owner)
	if previous == registrationLive {
		owner.store.publishDisposed(closeContext, owner.conversation)
	}
}
