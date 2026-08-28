package continuable

import (
	"context"
	"errors"
	"sync"

	"github.com/gorenx/goren/session"
)

// continuableChildRegistry indexes active Continuable child owners. Its mutex
// protects only membership and shutdown admission; child commands and I/O run
// through the per-child mailbox without holding it.
type continuableChildRegistry struct {
	dependencies Dependencies
	materializer *materializer
	mutex        sync.Mutex
	entries      map[session.SessionID]*continuableChild
	closing      bool
}

func newContinuableChildRegistry(
	dependencySet Dependencies,
	factory *materializer,
) *continuableChildRegistry {
	return &continuableChildRegistry{
		dependencies: dependencySet,
		materializer: factory,
		entries:      make(map[session.SessionID]*continuableChild),
	}
}

func (children *continuableChildRegistry) acquire(
	childID session.SessionID,
) (*continuableChild, error) {
	children.mutex.Lock()
	if children.closing {
		children.mutex.Unlock()
		return nil, errors.New("subagent: Continuable children are closing")
	}
	current := children.entries[childID]
	if current != nil {
		children.mutex.Unlock()
		return current, nil
	}
	current = newContinuableChild(
		children.dependencies,
		children.materializer,
		children,
		childID,
	)
	children.entries[childID] = current
	children.mutex.Unlock()
	go current.run()
	return current, nil
}

func (children *continuableChildRegistry) retire(
	child *continuableChild,
) {
	children.mutex.Lock()
	if children.entries[child.id] == child {
		delete(children.entries, child.id)
	}
	children.mutex.Unlock()
}

func (children *continuableChildRegistry) notify(
	childID session.SessionID,
) {
	children.mutex.Lock()
	child := children.entries[childID]
	children.mutex.Unlock()
	if child != nil {
		child.notify()
	}
}

func (children *continuableChildRegistry) interrupt(
	ctx context.Context,
	childID session.SessionID,
) error {
	children.mutex.Lock()
	child := children.entries[childID]
	children.mutex.Unlock()
	if child == nil {
		return nil
	}
	if err := child.interrupt(ctx); errors.Is(
		err,
		errChildRetired,
	) {
		return nil
	} else {
		return err
	}
}

func (children *continuableChildRegistry) close(ctx context.Context) error {
	children.mutex.Lock()
	children.closing = true
	targets := make([]*continuableChild, 0, len(children.entries))
	for _, child := range children.entries {
		targets = append(targets, child)
	}
	children.entries = nil
	children.mutex.Unlock()
	results := make(chan error, len(targets))
	var shutdowns sync.WaitGroup
	for _, child := range targets {
		shutdowns.Add(1)
		go func() {
			defer shutdowns.Done()
			results <- child.shutdown(ctx)
		}()
	}
	shutdowns.Wait()
	close(results)
	var closeErr error
	for err := range results {
		closeErr = errors.Join(closeErr, err)
	}
	return closeErr
}
