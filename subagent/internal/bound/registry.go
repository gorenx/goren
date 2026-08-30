package bound

import (
	"context"
	"errors"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
	subagentprojection "github.com/gorenx/goren/subagent/internal/projection"
)

type bindingKey struct {
	parentID       session.SessionID
	name           string
	childSessionID session.SessionID
}

// registry indexes Binding workers owned by exact parent Agent epochs. Its
// mutex protects only lookup and lifecycle membership.
type registry struct {
	dependencies Dependencies
	materializer *materializer
	definitions  *definitionCatalog
	ctx          context.Context
	cancel       context.CancelFunc
	mutex        sync.Mutex
	// Outer key is a user Session ID. Outer value is that Session's worker
	// index. Inner key is the stable Definition name; inner value is the sole
	// serial Binding worker for that parent and name.
	entries map[session.SessionID]map[string]*boundChild
	closing bool
}

func newRegistry(
	dependencySet Dependencies,
	factory *materializer,
) *registry {
	// Binding workers are closed explicitly with the Service. Their later Agent
	// construction must not inherit callback markers or caller cancellation.
	registryContext, cancelRegistry := context.WithCancel(context.Background())
	return &registry{
		dependencies: dependencySet,
		materializer: factory,
		ctx:          registryContext,
		cancel:       cancelRegistry,
		entries: make(
			map[session.SessionID]map[string]*boundChild,
		),
	}
}

func (children *registry) acquire(
	parentAgent agent.Agent,
	bindingValue subagentprojection.BoundBinding,
) (*boundChild, error) {
	key := bindingKey{
		parentID:       parentAgent.ID(),
		name:           bindingValue.Name,
		childSessionID: bindingValue.ChildSessionID,
	}
	children.mutex.Lock()
	if children.closing {
		children.mutex.Unlock()
		return nil, errors.New("subagent: Bound children are closing")
	}
	parentChildren := children.entries[key.parentID]
	current := parentChildren[key.name]
	if current != nil && agent.Same(current.parent, parentAgent) &&
		current.key.childSessionID == key.childSessionID {
		children.mutex.Unlock()
		return current, nil
	}
	next := newBoundChild(
		children.ctx,
		children.dependencies,
		children.materializer,
		children.definitions,
		parentAgent,
		bindingValue,
	)
	if parentChildren == nil {
		// Key is a stable Definition name. Value is the sole serial Binding
		// worker for that name in this exact user Session.
		parentChildren = make(map[string]*boundChild)
		children.entries[key.parentID] = parentChildren
	}
	parentChildren[key.name] = next
	children.mutex.Unlock()
	go next.run()
	if current != nil {
		current.requestDispose()
	}
	return next, nil
}

func (children *registry) interrupt(
	requestContext context.Context,
	childSessionID session.SessionID,
) error {
	children.mutex.Lock()
	var target *boundChild
	for _, parentChildren := range children.entries {
		for _, current := range parentChildren {
			if current.key.childSessionID == childSessionID {
				target = current
				break
			}
		}
		if target != nil {
			break
		}
	}
	children.mutex.Unlock()
	if target == nil {
		return nil
	}
	return target.interrupt(requestContext)
}

func (children *registry) agentDisposed(
	requestContext context.Context,
	subject agent.Agent,
) error {
	if subject == nil {
		return nil
	}
	if requestContext == nil {
		requestContext = context.Background()
	}
	children.mutex.Lock()
	parentChildren := children.entries[subject.ID()]
	targets := make([]*boundChild, 0, len(parentChildren))
	for definitionName, child := range parentChildren {
		if !agent.Same(child.parent, subject) {
			continue
		}
		targets = append(targets, child)
		delete(parentChildren, definitionName)
	}
	if len(parentChildren) == 0 {
		delete(children.entries, subject.ID())
	}
	children.mutex.Unlock()
	return disposeBoundChildren(requestContext, targets)
}

func (children *registry) close(
	closeContext context.Context,
) error {
	if closeContext == nil {
		closeContext = context.Background()
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
	children.cancel()
	results := make(chan error, len(targets))
	var shutdowns sync.WaitGroup
	for _, child := range targets {
		shutdowns.Add(1)
		go func() {
			defer shutdowns.Done()
			results <- child.shutdown(closeContext)
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

func disposeBoundChildren(
	requestContext context.Context,
	targets []*boundChild,
) error {
	results := make(chan error, len(targets))
	var disposals sync.WaitGroup
	for _, child := range targets {
		disposals.Add(1)
		go func() {
			defer disposals.Done()
			results <- child.dispose(requestContext)
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
