package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/gorenx/goren/session"
)

// ErrDescendantAdmissionClosed reports that an exact live parent no longer
// accepts construction of runtime descendants.
var ErrDescendantAdmissionClosed = errors.New(
	"agent: runtime parent is not accepting descendants",
)

// epoch is one exact Agent instance lifetime. Structural lifetime and event
// publication are explicit state axes because a close may race publication.
type epoch struct {
	coordinator *LifecycleCoordinator
	id          session.SessionID
	subject     Agent
	runtime     AgentScopeRuntime

	parent   *epoch
	children map[*epoch]struct{}

	phase               epochPhase
	publication         publicationPhase
	descendantAdmission descendantAdmission
	teardownOrigin      teardownOrigin
	teardown            *agentTeardown

	construction lifecycleSignal
	closing      lifecycleSignal
	closed       lifecycleSignal
	closeErr     error
}

func (target *epoch) ClosingSignal() <-chan struct{} {
	if target == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return target.closing.done
}

func (target *epoch) Attach(
	subject Agent,
	runtime AgentScopeRuntime,
) (AgentTeardown, error) {
	if target == nil || target.coordinator == nil {
		return nil, errors.New("agent: Agent epoch is unavailable")
	}
	if subject == nil || runtime == nil {
		return nil, errors.New("agent: Agent epoch requires an Agent and Agent runtime")
	}
	if subject.ID() != target.id {
		return nil, fmt.Errorf(
			"agent: Agent id %q does not match epoch id %q",
			subject.ID(),
			target.id,
		)
	}
	target.coordinator.mutex.Lock()
	defer target.coordinator.mutex.Unlock()
	if target.phase != epochMaterializing {
		return nil, fmt.Errorf(
			"agent: Agent %q cannot attach in epoch phase %d",
			target.id,
			target.phase,
		)
	}
	target.subject = subject
	target.runtime = runtime
	target.phase = epochAttached
	return target.teardown, nil
}

// LifecycleCoordinator owns every exact Agent epoch, Registry membership,
// publication, and runtime parent-child relation. Plugin topology is an
// implementation detail of the attached Agent runtime.
type LifecycleCoordinator struct {
	mutex  sync.Mutex
	byID   map[session.SessionID]*epoch
	epochs []*epoch
	report func(error)
}

func newLifecycleCoordinator(report func(error)) *LifecycleCoordinator {
	if report == nil {
		report = func(error) {}
	}
	return &LifecycleCoordinator{
		byID:   make(map[session.SessionID]*epoch),
		report: report,
	}
}

func (coordinator *LifecycleCoordinator) createEpoch(
	identifier session.SessionID,
	parent Agent,
) (*epoch, error) {
	if identifier == "" {
		return nil, errors.New("agent: Agent Session id is empty")
	}
	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	if _, exists := coordinator.byID[identifier]; exists {
		return nil, fmt.Errorf("agent: Agent %q already has an active epoch", identifier)
	}
	var parentEpoch *epoch
	if parent != nil {
		parentEpoch = coordinator.byID[parent.ID()]
		if parentEpoch == nil || !Same(parentEpoch.subject, parent) {
			return nil, errors.New(
				"agent: runtime parent is not an exact Agent in this Registry",
			)
		}
		if !coordinator.isVisibleLocked(parentEpoch) ||
			parentEpoch.descendantAdmission != descendantsAccepted {
			return nil, ErrDescendantAdmissionClosed
		}
	}
	target := &epoch{
		coordinator:         coordinator,
		id:                  identifier,
		parent:              parentEpoch,
		children:            make(map[*epoch]struct{}),
		phase:               epochMaterializing,
		publication:         publicationUnpublished,
		descendantAdmission: descendantsAccepted,
		construction:        newLifecycleSignal(),
		closing:             newLifecycleSignal(),
		closed:              newLifecycleSignal(),
	}
	target.teardown = &agentTeardown{
		coordinator: coordinator,
		epoch:       target,
	}
	coordinator.byID[identifier] = target
	coordinator.epochs = append(coordinator.epochs, target)
	if parentEpoch != nil {
		parentEpoch.children[target] = struct{}{}
	}
	return target, nil
}

func (coordinator *LifecycleCoordinator) activate(
	requestContext context.Context,
	target *epoch,
	startSource SessionStartSource,
) error {
	coordinator.mutex.Lock()
	if target.phase != epochAttached ||
		target.publication != publicationUnpublished ||
		target.subject == nil || target.runtime == nil {
		phase := target.phase
		coordinator.mutex.Unlock()
		return fmt.Errorf(
			"agent: Agent %q cannot publish from epoch phase %d",
			target.id,
			phase,
		)
	}
	target.publication = publicationPublishing
	subject := target.subject
	runtime := target.runtime
	coordinator.mutex.Unlock()

	publishErr := runtime.Dispatch(
		requestContext,
		Created{
			Subject: subject,
		},
	)
	coordinator.mutex.Lock()
	// Ordered publication may have reached earlier listeners before a later
	// listener returns an error and rejects the commit. Once dispatch starts,
	// treat the edge as published so abort emits the paired Disposed fact.
	target.publication = publicationPublished
	if publishErr != nil {
		coordinator.mutex.Unlock()
		return publishErr
	}
	if target.phase != epochAttached {
		phase := target.phase
		coordinator.mutex.Unlock()
		return fmt.Errorf(
			"agent: Agent %q publication interrupted in epoch phase %d",
			target.id,
			phase,
		)
	}
	coordinator.mutex.Unlock()

	if observerErr := runtime.Dispatch(
		requestContext,
		SessionStarted{
			Subject: subject,
			Source:  startSource,
		},
	); observerErr != nil {
		coordinator.reportObserverFailure(fmt.Errorf(
			"agent: Agent %q session-start observer: %w",
			target.id,
			observerErr,
		))
	}
	if requestErr := requestContext.Err(); requestErr != nil {
		return requestErr
	}

	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	if target.phase != epochAttached {
		return fmt.Errorf(
			"agent: Agent %q activation interrupted in epoch phase %d",
			target.id,
			target.phase,
		)
	}
	target.phase = epochLive
	target.construction.close()
	return nil
}

func (coordinator *LifecycleCoordinator) abort(
	closeContext context.Context,
	target *epoch,
) error {
	coordinator.mutex.Lock()
	target.construction.close()
	if target.runtime == nil {
		coordinator.finishClosedLocked(target, nil)
		coordinator.mutex.Unlock()
		return nil
	}
	coordinator.mutex.Unlock()
	return coordinator.closeExact(closeContext, target)
}

func (coordinator *LifecycleCoordinator) handle(target *epoch) Handle {
	return Handle{
		Subject:     target.subject,
		coordinator: coordinator,
		epoch:       target,
	}
}

func (coordinator *LifecycleCoordinator) closeExact(
	closeContext context.Context,
	target *epoch,
) error {
	if closeContext == nil {
		closeContext = context.Background()
	}
	coordinator.mutex.Lock()
	switch target.phase {
	case epochClosed:
		closeErr := target.closeErr
		coordinator.mutex.Unlock()
		return closeErr
	case epochClosing:
		closed := target.closed.done
		coordinator.mutex.Unlock()
		select {
		case <-closed:
			coordinator.mutex.Lock()
			closeErr := target.closeErr
			coordinator.mutex.Unlock()
			return closeErr
		case <-closeContext.Done():
			return context.Cause(closeContext)
		}
	default:
		target.phase = epochClosing
		target.descendantAdmission = descendantsClosing
		target.teardownOrigin = teardownByCoordinator
		target.closing.close()
	}
	constructionDone := target.construction.done
	coordinator.mutex.Unlock()

	// Once this caller has claimed the close transaction, cancellation may no
	// longer abandon the epoch in Closing. Other callers may stop waiting on
	// their own Context, but the owner must finish descendant closure and runtime
	// teardown so a later epoch can safely reuse the durable Session id.
	completionContext := context.WithoutCancel(closeContext)
	<-constructionDone

	coordinator.mutex.Lock()
	children := make([]*epoch, 0, len(target.children))
	for child := range target.children {
		children = append(children, child)
	}
	runtime := target.runtime
	published := target.publication == publicationPublished
	coordinator.mutex.Unlock()

	var closeErr error
	for index := len(children) - 1; index >= 0; index-- {
		closeErr = errors.Join(
			closeErr,
			coordinator.closeExact(completionContext, children[index]),
		)
	}
	if published && runtime != nil {
		coordinator.retirePublication(completionContext, target)
	}
	if runtime != nil {
		closeErr = errors.Join(closeErr, runtime.Teardown(completionContext))
	}
	coordinator.mutex.Lock()
	coordinator.finishClosedLocked(target, closeErr)
	coordinator.mutex.Unlock()
	return closeErr
}

func (coordinator *LifecycleCoordinator) runtimeTeardownStarted(
	closeContext context.Context,
	target *epoch,
) {
	coordinator.mutex.Lock()
	if coordinator.byID[target.id] != target {
		coordinator.mutex.Unlock()
		return
	}
	if target.phase != epochClosed && target.phase != epochClosing {
		target.phase = epochClosing
		target.descendantAdmission = descendantsClosing
		target.teardownOrigin = teardownByRuntime
		target.closing.close()
	}
	coordinator.mutex.Unlock()
	coordinator.retirePublication(closeContext, target)
}

func (coordinator *LifecycleCoordinator) runtimeTeardownFinished(
	target *epoch,
	closeErr error,
) {
	coordinator.mutex.Lock()
	if coordinator.byID[target.id] != target {
		coordinator.mutex.Unlock()
		return
	}
	if target.teardownOrigin != teardownByRuntime {
		coordinator.mutex.Unlock()
		return
	}
	coordinator.finishClosedLocked(target, closeErr)
	coordinator.mutex.Unlock()
}

func (coordinator *LifecycleCoordinator) retirePublication(
	requestContext context.Context,
	target *epoch,
) {
	coordinator.mutex.Lock()
	if target.publication != publicationPublished || target.runtime == nil {
		coordinator.mutex.Unlock()
		return
	}
	target.publication = publicationRetired
	runtime := target.runtime
	subject := target.subject
	coordinator.mutex.Unlock()
	if requestContext == nil {
		requestContext = context.Background()
	}
	if observerErr := runtime.Dispatch(
		requestContext,
		Disposed{
			Subject: subject,
		},
	); observerErr != nil {
		coordinator.reportObserverFailure(fmt.Errorf(
			"agent: Agent %q disposed observer: %w",
			target.id,
			observerErr,
		))
	}
}

func (coordinator *LifecycleCoordinator) hasDescendants(parent Agent) bool {
	parentEpoch, err := coordinator.epochForAgent(parent)
	if err != nil {
		return false
	}
	coordinator.mutex.Lock()
	present := len(parentEpoch.children) != 0
	coordinator.mutex.Unlock()
	return present
}

func (coordinator *LifecycleCoordinator) cancelConstructions() {
	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	for _, target := range coordinator.epochs {
		if target.phase == epochMaterializing || target.phase == epochAttached {
			target.closing.close()
		}
	}
}

func (coordinator *LifecycleCoordinator) cancelEpochConstructions(
	targets []*epoch,
) {
	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	for _, target := range targets {
		if target == nil || target.coordinator != coordinator {
			continue
		}
		if target.phase == epochMaterializing || target.phase == epochAttached {
			target.closing.close()
		}
	}
}

func (coordinator *LifecycleCoordinator) closeAll(
	closeContext context.Context,
) error {
	coordinator.mutex.Lock()
	roots := make([]*epoch, 0)
	for _, candidate := range coordinator.epochs {
		if candidate.parent == nil {
			roots = append(roots, candidate)
		}
	}
	coordinator.mutex.Unlock()
	var closeErr error
	for index := len(roots) - 1; index >= 0; index-- {
		closeErr = errors.Join(
			closeErr,
			coordinator.closeExact(closeContext, roots[index]),
		)
	}
	return closeErr
}

func (coordinator *LifecycleCoordinator) liveAgent(
	identifier session.SessionID,
) (Agent, bool) {
	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	target := coordinator.byID[identifier]
	if target == nil || !coordinator.isVisibleLocked(target) {
		return nil, false
	}
	return target.subject, true
}

func (coordinator *LifecycleCoordinator) contains(subject Agent) bool {
	if subject == nil {
		return false
	}
	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	target := coordinator.byID[subject.ID()]
	return target != nil && Same(target.subject, subject) &&
		coordinator.isVisibleLocked(target)
}

func (coordinator *LifecycleCoordinator) liveAgents() []Agent {
	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	result := make([]Agent, 0, len(coordinator.epochs))
	for _, candidate := range coordinator.epochs {
		if coordinator.isVisibleLocked(candidate) {
			result = append(result, candidate.subject)
		}
	}
	return result
}

func (*LifecycleCoordinator) isVisibleLocked(target *epoch) bool {
	if target == nil || target.subject == nil {
		return false
	}
	if target.phase == epochLive {
		return true
	}
	return target.phase == epochAttached &&
		(target.publication == publicationPublishing ||
			target.publication == publicationPublished)
}

func (coordinator *LifecycleCoordinator) epochForAgent(
	subject Agent,
) (*epoch, error) {
	if subject == nil {
		return nil, errors.New("agent: Agent subject is nil")
	}
	coordinator.mutex.Lock()
	target := coordinator.byID[subject.ID()]
	exact := target != nil && Same(target.subject, subject)
	coordinator.mutex.Unlock()
	if !exact {
		return nil, fmt.Errorf(
			"agent: Agent %q is not an exact epoch in this Registry",
			subject.ID(),
		)
	}
	return target, nil
}

func (coordinator *LifecycleCoordinator) finishClosedLocked(
	target *epoch,
	closeErr error,
) {
	if target.phase == epochClosed {
		return
	}
	target.construction.close()
	target.closing.close()
	target.phase = epochClosed
	target.closeErr = closeErr
	if target.parent != nil {
		delete(target.parent.children, target)
	}
	if coordinator.byID[target.id] == target {
		delete(coordinator.byID, target.id)
		coordinator.epochs = slices.DeleteFunc(
			coordinator.epochs,
			func(candidate *epoch) bool {
				return candidate == target
			},
		)
	}
	target.closed.close()
}

func (coordinator *LifecycleCoordinator) reportObserverFailure(problem error) {
	if problem == nil {
		return
	}
	defer func() { _ = recover() }()
	coordinator.report(problem)
}

var _ AgentEpoch = (*epoch)(nil)
var _ AgentTeardown = (*agentTeardown)(nil)
