package session

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/gorenx/goren/plugin"
)

// registration owns one Session's membership in MemoryStore.
type registration struct {
	mu              sync.Mutex
	store           *MemoryStore
	conversation    *coordinator
	live            bool
	announced       bool
	announcing      bool
	detachRequested bool
	releaseComplete bool
	releaseDone     chan struct{}
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
	return owner.live
}

func (owner *registration) announce(requestContext context.Context) error {
	owner.mu.Lock()
	if owner.announced || owner.announcing {
		owner.mu.Unlock()
		return fmt.Errorf("session: %q was already announced", owner.conversation.ID())
	}
	owner.announced = true
	owner.announcing = true
	owner.mu.Unlock()

	dispatchErr := owner.store.publishCreated(requestContext, owner.conversation)
	owner.finishAnnouncement(requestContext)
	return dispatchErr
}

func (owner *registration) publishAppend(requestContext context.Context, committed Event) {
	owner.mu.Lock()
	live := owner.live
	owner.mu.Unlock()
	if !live {
		return
	}
	publishErr := safelyDispatch(func() error {
		return plugin.Publish(
			requestContext,
			owner.store,
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
		if !owner.live || owner.releaseComplete {
			owner.mu.Unlock()
			return nil
		}
		if owner.releaseDone != nil {
			done := owner.releaseDone
			owner.mu.Unlock()
			select {
			case <-done:
				continue
			case <-closeContext.Done():
				return context.Cause(closeContext)
			}
		}
		done := make(chan struct{})
		owner.releaseDone = done
		owner.mu.Unlock()

		releaseErr := owner.finishRelease(closeContext)
		owner.mu.Lock()
		if releaseErr == nil {
			owner.releaseComplete = true
		}
		owner.releaseDone = nil
		close(done)
		owner.mu.Unlock()
		return releaseErr
	}
}

func (owner *registration) finishRelease(closeContext context.Context) error {
	barrier, err := owner.conversation.sealWrites(closeContext)
	if err != nil {
		return err
	}
	if err := owner.store.publishFlush(closeContext, owner.conversation, barrier); err != nil {
		if !errors.Is(err, plugin.ErrPluginNotActive) &&
			!errors.Is(err, plugin.ErrPluginNotBound) {
			return err
		}
	}
	owner.detach(closeContext)
	return nil
}

func (owner *registration) rollback(closeContext context.Context) error {
	releaseErr := owner.release(closeContext)
	owner.detach(closeContext)
	return releaseErr
}

func (owner *registration) finishAnnouncement(requestContext context.Context) {
	owner.mu.Lock()
	owner.announcing = false
	shouldDetach := owner.detachRequested
	if shouldDetach {
		owner.live = false
		owner.detachRequested = false
	}
	owner.mu.Unlock()
	if shouldDetach {
		owner.store.removeRegistration(owner)
		owner.conversation.detach(owner)
		owner.store.publishDisposed(requestContext, owner.conversation)
	}
}

func (owner *registration) detach(closeContext context.Context) {
	owner.mu.Lock()
	if !owner.live {
		owner.mu.Unlock()
		return
	}
	if owner.announcing {
		owner.detachRequested = true
		owner.mu.Unlock()
		return
	}
	owner.live = false
	announced := owner.announced
	owner.mu.Unlock()
	owner.store.removeRegistration(owner)
	owner.conversation.detach(owner)
	if announced {
		owner.store.publishDisposed(closeContext, owner.conversation)
	}
}
