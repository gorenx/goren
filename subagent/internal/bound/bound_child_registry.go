package bound

import (
	"context"
	"errors"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
)

type boundChildKey struct {
	parentID session.SessionID
	childID  session.SessionID
}

// boundChildRegistry indexes the Bound children owned by exact parent Agent
// epochs. Its mutex protects only lookup and lifecycle membership; it never
// surrounds child work or I/O.
type boundChildRegistry struct {
	dependencies Dependencies
	materializer *materializer
	mutex        sync.Mutex
	entries      map[session.SessionID]map[session.SessionID]*boundChild
	closing      bool
}

func newBoundChildRegistry(
	dependencySet Dependencies,
	factory *materializer,
) *boundChildRegistry {
	return &boundChildRegistry{
		dependencies: dependencySet,
		materializer: factory,
		entries:      make(map[session.SessionID]map[session.SessionID]*boundChild),
	}
}

func (children *boundChildRegistry) acquire(
	parentAgent agent.Agent,
	childID session.SessionID,
) (*boundChild, error) {
	key := boundChildKey{
		parentID: parentAgent.ID(),
		childID:  childID,
	}
	children.mutex.Lock()
	if children.closing {
		children.mutex.Unlock()
		return nil, errors.New("subagent: Bound children are closing")
	}
	parentChildren := children.entries[key.parentID]
	current := parentChildren[key.childID]
	if current != nil && agent.Same(current.parent, parentAgent) {
		children.mutex.Unlock()
		return current, nil
	}
	next := newBoundChild(
		children.dependencies,
		children.materializer,
		parentAgent,
		childID,
	)
	if parentChildren == nil {
		parentChildren = make(map[session.SessionID]*boundChild)
		children.entries[key.parentID] = parentChildren
	}
	parentChildren[key.childID] = next
	children.mutex.Unlock()
	go next.run()
	if current != nil {
		current.requestDispose()
	}
	return next, nil
}

func (children *boundChildRegistry) notifyParent(parentID session.SessionID) {
	children.mutex.Lock()
	parentChildren := children.entries[parentID]
	targets := make([]*boundChild, 0, len(parentChildren))
	for _, child := range parentChildren {
		targets = append(targets, child)
	}
	children.mutex.Unlock()
	for _, child := range targets {
		child.notify()
	}
}

func (children *boundChildRegistry) interrupt(
	ctx context.Context,
	childID session.SessionID,
) error {
	children.mutex.Lock()
	var target *boundChild
	for _, parentChildren := range children.entries {
		if current := parentChildren[childID]; current != nil {
			target = current
			break
		}
	}
	children.mutex.Unlock()
	if target == nil {
		return nil
	}
	return target.interrupt(ctx)
}

func (children *boundChildRegistry) agentDisposed(
	ctx context.Context,
	subject agent.Agent,
) error {
	if subject == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	children.mutex.Lock()
	parentChildren := children.entries[subject.ID()]
	targets := make([]*boundChild, 0, len(parentChildren))
	for childID, child := range parentChildren {
		if !agent.Same(child.parent, subject) {
			continue
		}
		targets = append(targets, child)
		delete(parentChildren, childID)
	}
	if len(parentChildren) == 0 {
		delete(children.entries, subject.ID())
	}
	children.mutex.Unlock()
	return disposeBoundChildren(ctx, targets)
}

func (children *boundChildRegistry) close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	children.mutex.Lock()
	children.closing = true
	targets := make([]*boundChild, 0)
	for _, parentChildren := range children.entries {
		for _, child := range parentChildren {
			targets = append(targets, child)
		}
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

func (children *boundChildRegistry) find(
	parentID session.SessionID,
	childID session.SessionID,
) *boundChild {
	children.mutex.Lock()
	defer children.mutex.Unlock()
	return children.entries[parentID][childID]
}

func disposeBoundChildren(
	ctx context.Context,
	targets []*boundChild,
) error {
	results := make(chan error, len(targets))
	var disposals sync.WaitGroup
	for _, child := range targets {
		disposals.Add(1)
		go func() {
			defer disposals.Done()
			results <- child.dispose(ctx)
		}()
	}
	disposals.Wait()
	close(results)
	var disposeErr error
	for err := range results {
		disposeErr = errors.Join(disposeErr, err)
	}
	return disposeErr
}
