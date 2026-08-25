package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/gorenx/goren/session"
)

// epoch is one exact Agent instance lifetime. Structural lifetime and event
// publication are explicit state axes because a close may race publication.
type epoch struct {
	id      session.SessionID
	subject Agent
	runtime AgentScopeRuntime

	parent   *epoch
	children map[*epoch]struct{}

	phase               epochPhase
	publication         publicationPhase
	descendantAdmission descendantAdmission
	teardownOrigin      teardownOrigin
	lifecycle           *lifecycle

	construction lifecycleSignal
	closing      lifecycleSignal
	closed       lifecycleSignal
	closeErr     error
}

// LifecycleCoordinator owns every exact Agent epoch, Registry membership,
// publication, and runtime parent-child relation. Plugin topology is an
// implementation detail of the attached Agent runtime.
type LifecycleCoordinator struct {
	mutex     sync.Mutex
	admission registryAdmission
	byID      map[session.SessionID]*epoch
	epochs    []*epoch
	report    func(error)
}

func newLifecycleCoordinator(report func(error)) *LifecycleCoordinator {
	if report == nil {
		report = func(error) {}
	}
	return &LifecycleCoordinator{
		admission: registryAccepting,
		byID:      make(map[session.SessionID]*epoch),
		report:    report,
	}
}

type reservation struct {
	coordinator *LifecycleCoordinator
	epoch       *epoch
}

func (pending *reservation) ClosingSignal() <-chan struct{} {
	if pending == nil || pending.epoch == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return pending.epoch.closing.done
}

func (pending *reservation) Attach(
	subject Agent,
	runtime AgentScopeRuntime,
) (Lifecycle, error) {
	if pending == nil || pending.coordinator == nil || pending.epoch == nil {
		return nil, errors.New("agent: Reservation is unavailable")
	}
	if subject == nil || runtime == nil {
		return nil, errors.New("agent: Reservation requires an Agent and Agent runtime")
	}
	if subject.ID() != pending.epoch.id {
		return nil, fmt.Errorf(
			"agent: Agent id %q does not match reserved id %q",
			subject.ID(),
			pending.epoch.id,
		)
	}
	pending.coordinator.mutex.Lock()
	defer pending.coordinator.mutex.Unlock()
	if pending.epoch.phase != epochMaterializing {
		return nil, fmt.Errorf(
			"agent: Agent %q cannot attach in epoch phase %d",
			pending.epoch.id,
			pending.epoch.phase,
		)
	}
	pending.epoch.subject = subject
	pending.epoch.runtime = runtime
	pending.epoch.phase = epochAttached
	return pending.epoch.lifecycle, nil
}

type lifecycle struct {
	coordinator *LifecycleCoordinator
	epoch       *epoch
}

func (instance *lifecycle) BeginTeardown(closeContext context.Context) {
	if instance == nil || instance.coordinator == nil || instance.epoch == nil {
		return
	}
	instance.coordinator.runtimeTeardownStarted(
		closeContext,
		instance.epoch,
	)
}

func (instance *lifecycle) FinishTeardown(closeErr error) {
	if instance == nil || instance.coordinator == nil || instance.epoch == nil {
		return
	}
	instance.coordinator.runtimeTeardownFinished(
		instance.epoch,
		closeErr,
	)
}

func (coordinator *LifecycleCoordinator) reserve(
	identifier session.SessionID,
	parent Agent,
) (*reservation, error) {
	if identifier == "" {
		return nil, errors.New("agent: Agent Session id is empty")
	}
	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	if coordinator.admission != registryAccepting {
		return nil, errors.New("agent: Agent lifecycle is shutting down")
	}
	if _, exists := coordinator.byID[identifier]; exists {
		return nil, fmt.Errorf("agent: Agent %q is already reserved", identifier)
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
			return nil, errors.New(
				"agent: runtime parent is not accepting descendants",
			)
		}
	}
	reserved := &epoch{
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
	reserved.lifecycle = &lifecycle{
		coordinator: coordinator,
		epoch:       reserved,
	}
	coordinator.byID[identifier] = reserved
	coordinator.epochs = append(coordinator.epochs, reserved)
	if parentEpoch != nil {
		parentEpoch.children[reserved] = struct{}{}
	}
	return &reservation{
		coordinator: coordinator,
		epoch:       reserved,
	}, nil
}

func (coordinator *LifecycleCoordinator) activate(
	requestContext context.Context,
	pending *reservation,
	startSource SessionStartSource,
) error {
	target := pending.epoch
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
	if publishErr != nil {
		target.publication = publicationUnpublished
		coordinator.mutex.Unlock()
		return publishErr
	}
	target.publication = publicationPublished
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
	pending *reservation,
) error {
	target := pending.epoch
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

func (coordinator *LifecycleCoordinator) handle(pending *reservation) Handle {
	return Handle{
		Subject:   pending.epoch.subject,
		lifecycle: pending.epoch.lifecycle,
	}
}

func (instance *lifecycle) Dispose(closeContext context.Context) error {
	if instance == nil || instance.coordinator == nil || instance.epoch == nil {
		return nil
	}
	return instance.coordinator.closeExact(closeContext, instance.epoch)
}

func (instance *lifecycle) ClosingSignal() <-chan struct{} {
	if instance == nil || instance.epoch == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return instance.epoch.closing.done
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
		target.descendantAdmission = descendantsDraining
		target.teardownOrigin = teardownByCoordinator
		target.closing.close()
	}
	constructionDone := target.construction.done
	coordinator.mutex.Unlock()

	select {
	case <-constructionDone:
	case <-closeContext.Done():
		return context.Cause(closeContext)
	}

	coordinator.mutex.Lock()
	children := make([]*epoch, 0, len(target.children))
	for child := range target.children {
		children = append(children, child)
	}
	runtime := target.runtime
	published := target.publication == publicationPublished
	coordinator.mutex.Unlock()

	drainContext := context.WithoutCancel(closeContext)
	var closeErr error
	for index := len(children) - 1; index >= 0; index-- {
		closeErr = errors.Join(
			closeErr,
			coordinator.closeExact(drainContext, children[index]),
		)
	}
	if published && runtime != nil {
		coordinator.retirePublication(drainContext, target)
	}
	if runtime != nil {
		closeErr = errors.Join(closeErr, runtime.Teardown(drainContext))
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
		target.descendantAdmission = descendantsDraining
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

func (coordinator *LifecycleCoordinator) closeDescendants(
	closeContext context.Context,
	parent Agent,
) error {
	parentEpoch, err := coordinator.epochForAgent(parent)
	if err != nil {
		return err
	}
	coordinator.mutex.Lock()
	parentEpoch.descendantAdmission = descendantsDraining
	children := make([]*epoch, 0, len(parentEpoch.children))
	for child := range parentEpoch.children {
		children = append(children, child)
	}
	coordinator.mutex.Unlock()
	var closeErr error
	for index := len(children) - 1; index >= 0; index-- {
		closeErr = errors.Join(
			closeErr,
			coordinator.closeExact(closeContext, children[index]),
		)
	}
	return closeErr
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

func (coordinator *LifecycleCoordinator) beginShutdown() {
	coordinator.mutex.Lock()
	coordinator.admission = registryShuttingDown
	coordinator.mutex.Unlock()
}

func (coordinator *LifecycleCoordinator) closeAll(
	closeContext context.Context,
) error {
	coordinator.beginShutdown()
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
		target.publication == publicationPublishing
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

func (coordinator *LifecycleCoordinator) assertClosed() error {
	coordinator.mutex.Lock()
	dangling := len(coordinator.byID)
	coordinator.mutex.Unlock()
	if dangling != 0 {
		return fmt.Errorf(
			"agent: Registry stopped with %d live Agent lifecycle(s)",
			dangling,
		)
	}
	return nil
}

func (coordinator *LifecycleCoordinator) reportObserverFailure(problem error) {
	if problem == nil {
		return
	}
	defer func() { _ = recover() }()
	coordinator.report(problem)
}

var _ Reservation = (*reservation)(nil)
var _ Lifecycle = (*lifecycle)(nil)
var _ managedLifecycle = (*lifecycle)(nil)
