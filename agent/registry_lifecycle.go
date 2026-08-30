package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/gorenx/goren/session"
)

// ErrDescendantAdmissionClosed reports that a live parent no longer accepts
// runtime descendants.
var ErrDescendantAdmissionClosed = errors.New(
	"agent: runtime parent is not accepting descendants",
)

// construction is one admitted Factory call and its cancellation relay. It
// owns no Registry or Host reference.
type construction struct {
	lifetime  *agentLifetime
	context   context.Context
	cancel    context.CancelCauseFunc
	relayDone chan struct{}
	once      sync.Once
}

func newConstruction(
	lifetime *agentLifetime,
	constructionContext context.Context,
	cancel context.CancelCauseFunc,
	relayDone chan struct{},
) *construction {
	return &construction{
		lifetime:  lifetime,
		context:   constructionContext,
		cancel:    cancel,
		relayDone: relayDone,
	}
}

func (admitted *construction) Context() context.Context {
	return admitted.context
}

func (admitted *construction) AgentLifetime() *agentLifetime {
	return admitted.lifetime
}

func (admitted *construction) Finish() bool {
	if admitted == nil {
		return false
	}
	finished := false
	admitted.once.Do(func() {
		admitted.cancel(nil)
		<-admitted.relayDone
		finished = true
	})
	return finished
}

func (service *RegistryService) beginConstruction(
	requestContext context.Context,
	identifier session.SessionID,
	parent Agent,
) (*construction, Factory, error) {
	if requestContext == nil {
		return nil, nil, errors.New("agent: construction Context is nil")
	}
	if identifier == "" {
		return nil, nil, errors.New("agent: Agent Session id is empty")
	}
	service.mutex.Lock()
	defer service.mutex.Unlock()
	if service.admission == registryInactive {
		return nil, nil, errors.New("agent: Agent Registry is not active")
	}
	if service.admission == registryDraining {
		return nil, nil, errors.New("agent: Agent Registry is shutting down")
	}
	if service.factory == nil {
		return nil, nil, errors.New("agent: Agent Registry is not active")
	}
	if _, exists := service.byID[identifier]; exists {
		return nil, nil, fmt.Errorf("agent: Agent %q is already active", identifier)
	}
	if parent != nil {
		parentLifetime := service.byID[parent.ID()]
		if parentLifetime == nil || !parentLifetime.Matches(parent) {
			return nil, nil, errors.New(
				"agent: runtime parent is not an exact Agent in this Registry",
			)
		}
		if !parentLifetime.AcceptsDescendants() {
			return nil, nil, ErrDescendantAdmissionClosed
		}
	}
	lifetime := newAgentLifetime(identifier)
	service.byID[identifier] = lifetime
	service.lifetimes = append(service.lifetimes, lifetime)
	if parent != nil {
		service.parentByChild[identifier] = parent.ID()
		childIDs := service.childrenByParent[parent.ID()]
		if childIDs == nil {
			childIDs = make(map[session.SessionID]struct{})
			service.childrenByParent[parent.ID()] = childIDs
		}
		childIDs[identifier] = struct{}{}
	}
	operationContext, cancelOperation := context.WithCancelCause(requestContext)
	relayDone := make(chan struct{})
	go func() {
		defer close(relayDone)
		select {
		case <-lifetime.ClosingSignal():
			cancelOperation(errors.New("agent: Agent construction is closing"))
		case <-operationContext.Done():
		}
	}()
	service.constructions[lifetime] = struct{}{}
	return newConstruction(
		lifetime,
		operationContext,
		cancelOperation,
		relayDone,
	), service.factory, nil
}

func (service *RegistryService) finishConstruction(admitted *construction) {
	if admitted == nil || !admitted.Finish() {
		return
	}
	lifetime := admitted.AgentLifetime()
	service.mutex.Lock()
	delete(service.constructions, lifetime)
	service.mutex.Unlock()
	lifetime.FinishConstruction()
}

func (service *RegistryService) construct(
	requestContext context.Context,
	identifier session.SessionID,
	parent Agent,
	contribution Setup,
	startSource SessionStartSource,
	build func(context.Context, Factory) (Host, error),
) (Handle, error) {
	admitted, agentFactory, err := service.beginConstruction(
		requestContext,
		identifier,
		parent,
	)
	if err != nil {
		return Handle{}, err
	}
	defer service.finishConstruction(admitted)
	lifetime := admitted.AgentLifetime()
	constructionContext := admitted.Context()
	agentHost, err := build(constructionContext, agentFactory)
	if err == nil {
		err = lifetime.Attach(agentHost)
	}
	if err == nil && contribution != nil {
		_, err = lifetime.ApplySetup(constructionContext, contribution)
	}
	if err == nil {
		err = agentHost.EnterServing(constructionContext)
	}
	if err == nil {
		err = agentHost.Announce(constructionContext)
	}
	if err == nil {
		err = service.publish(constructionContext, lifetime, startSource)
	}
	if err != nil {
		closeErr := service.failConstruction(lifetime, agentHost)
		return Handle{}, errors.Join(err, closeErr)
	}
	return Handle{
		Subject:  lifetime.Agent(),
		registry: service,
		lifetime: lifetime,
	}, nil
}

// Create constructs and publishes one fresh exact Agent instance.
func (service *RegistryService) Create(
	requestContext context.Context,
	creation CreateOptions,
) (Handle, error) {
	return service.construct(
		requestContext,
		creation.SessionID,
		creation.RuntimeParent,
		creation.Setup,
		SessionStartup,
		func(buildContext context.Context, agentFactory Factory) (Host, error) {
			return agentFactory.CreateAgent(
				buildContext,
				CreateHostOptions{
					SessionID:    creation.SessionID,
					Metadata:     creation.Metadata,
					Seed:         creation.Seed,
					AgentOptions: creation.AgentOptions,
				},
			)
		},
	)
}

// Resume reconstructs and publishes one durable exact Agent instance.
func (service *RegistryService) Resume(
	requestContext context.Context,
	restoration ResumeOptions,
) (Handle, error) {
	return service.construct(
		requestContext,
		restoration.SessionID,
		restoration.RuntimeParent,
		restoration.Setup,
		SessionResume,
		func(buildContext context.Context, agentFactory Factory) (Host, error) {
			return agentFactory.ResumeAgent(
				buildContext,
				ResumeHostOptions{
					SessionID:    restoration.SessionID,
					AgentOptions: restoration.AgentOptions,
				},
			)
		},
	)
}

func (service *RegistryService) publish(
	requestContext context.Context,
	lifetime *agentLifetime,
	startSource SessionStartSource,
) error {
	if err := lifetime.BeginPublication(); err != nil {
		return err
	}
	err := lifetime.DispatchLifecycleEvent(
		requestContext,
		Created{
			Subject: lifetime.Agent(),
		},
	)
	completionErr := lifetime.CompleteCreatedEvent()
	if err != nil {
		return err
	}
	if completionErr != nil {
		return completionErr
	}
	if observerErr := lifetime.DispatchLifecycleEvent(
		requestContext,
		SessionStarted{
			Subject: lifetime.Agent(),
			Source:  startSource,
		},
	); observerErr != nil {
		service.reportObserverFailure(fmt.Errorf(
			"agent: Agent %q session-start observer: %w",
			lifetime.SessionID(),
			observerErr,
		))
	}
	if err = requestContext.Err(); err != nil {
		return err
	}
	return lifetime.EnterLive()
}

func (service *RegistryService) closeLifetime(
	closeContext context.Context,
	lifetime *agentLifetime,
) error {
	if closeContext == nil {
		closeContext = context.Background()
	}
	admission := lifetime.BeginClose()
	closed := lifetime.ClosedSignal()
	if admission == lifetimeCloseFinished {
		return lifetime.CloseResult()
	}
	if admission == lifetimeCloseRunning {
		deferWait := service.subtreeDispatching(lifetime)
		if deferWait {
			return nil
		}
		select {
		case <-closed:
			return lifetime.CloseResult()
		case <-closeContext.Done():
			return context.Cause(closeContext)
		}
	}
	deferWait := service.subtreeDispatching(lifetime)
	go service.finishLifetimeClose(lifetime)
	if deferWait {
		return nil
	}
	select {
	case <-closed:
		return lifetime.CloseResult()
	case <-closeContext.Done():
		return context.Cause(closeContext)
	}
}

// subtreeDispatching reports whether synchronous event observation in
// this Agent lifetime tree must return before child-first close can progress.
func (service *RegistryService) subtreeDispatching(
	lifetime *agentLifetime,
) bool {
	service.mutex.Lock()
	defer service.mutex.Unlock()
	return service.subtreeDispatchingLocked(lifetime)
}

func (service *RegistryService) subtreeDispatchingLocked(
	lifetime *agentLifetime,
) bool {
	if lifetime.Dispatching() {
		return true
	}
	for childID := range service.childrenByParent[lifetime.SessionID()] {
		child := service.byID[childID]
		if child != nil && service.subtreeDispatchingLocked(child) {
			return true
		}
	}
	return false
}

func (service *RegistryService) finishLifetimeClose(lifetime *agentLifetime) {
	<-lifetime.ConstructionDone()
	if lifetime.IsClosed() {
		return
	}
	completionContext := context.Background()
	childLifetimes := service.children(lifetime.SessionID())
	var closeErr error
	for index := len(childLifetimes) - 1; index >= 0; index-- {
		closeErr = errors.Join(
			closeErr,
			service.closeLifetime(completionContext, childLifetimes[index]),
		)
	}
	service.retirePublication(completionContext, lifetime)
	_, hostCloseErr := lifetime.CloseOwnedHost(completionContext)
	closeErr = errors.Join(closeErr, hostCloseErr)
	service.finishClosed(lifetime, closeErr)
}

func (service *RegistryService) failConstruction(
	lifetime *agentLifetime,
	constructedHost Host,
) error {
	lifetime.BeginClose()
	completionContext := context.Background()
	childLifetimes := service.children(lifetime.SessionID())
	var closeErr error
	for index := len(childLifetimes) - 1; index >= 0; index-- {
		closeErr = errors.Join(
			closeErr,
			service.closeLifetime(completionContext, childLifetimes[index]),
		)
	}
	service.retirePublication(completionContext, lifetime)
	closedOwnedHost, hostCloseErr := lifetime.CloseOwnedHost(completionContext)
	closeErr = errors.Join(closeErr, hostCloseErr)
	if !closedOwnedHost && constructedHost != nil {
		closeErr = errors.Join(
			closeErr,
			constructedHost.Close(completionContext),
		)
	}
	service.finishClosed(lifetime, closeErr)
	return closeErr
}

func (service *RegistryService) children(
	parentID session.SessionID,
) []*agentLifetime {
	service.mutex.Lock()
	defer service.mutex.Unlock()
	result := make([]*agentLifetime, 0)
	for _, candidate := range service.lifetimes {
		if service.parentByChild[candidate.SessionID()] == parentID &&
			!candidate.IsClosed() {
			result = append(result, candidate)
		}
	}
	return result
}

func (service *RegistryService) retirePublication(
	requestContext context.Context,
	lifetime *agentLifetime,
) {
	subject, retiring := lifetime.BeginRetirement()
	if !retiring {
		return
	}
	if err := lifetime.DispatchLifecycleEvent(
		requestContext,
		Disposed{
			Subject: subject,
		},
	); err != nil {
		service.reportObserverFailure(fmt.Errorf(
			"agent: Agent %q disposed observer: %w",
			lifetime.SessionID(),
			err,
		))
	}
}

func (service *RegistryService) finishClosed(
	lifetime *agentLifetime,
	closeErr error,
) {
	service.mutex.Lock()
	defer service.mutex.Unlock()
	if lifetime.IsClosed() {
		return
	}
	identifier := lifetime.SessionID()
	if parentID, exists := service.parentByChild[identifier]; exists {
		delete(service.parentByChild, identifier)
		delete(service.childrenByParent[parentID], identifier)
		if len(service.childrenByParent[parentID]) == 0 {
			delete(service.childrenByParent, parentID)
		}
	}
	delete(service.childrenByParent, identifier)
	if service.byID[identifier] == lifetime {
		delete(service.byID, identifier)
		service.lifetimes = slices.DeleteFunc(
			service.lifetimes,
			func(candidate *agentLifetime) bool {
				return candidate == lifetime
			},
		)
	}
	lifetime.EnterClosed(closeErr)
}

func (service *RegistryService) deactivate(closeContext context.Context) error {
	if closeContext == nil {
		closeContext = context.Background()
	}
	service.mutex.Lock()
	switch service.admission {
	case registryInactive:
		closeErr := service.deactivationErr
		service.mutex.Unlock()
		return closeErr
	case registryDraining:
		deactivationDone := service.deactivation.Done()
		service.mutex.Unlock()
		select {
		case <-deactivationDone:
			service.mutex.Lock()
			closeErr := service.deactivationErr
			service.mutex.Unlock()
			return closeErr
		case <-closeContext.Done():
			return context.Cause(closeContext)
		}
	case registryAccepting:
		service.admission = registryDraining
		service.factory = nil
	}
	roots := make([]*agentLifetime, 0)
	for _, lifetime := range service.lifetimes {
		if _, child := service.parentByChild[lifetime.SessionID()]; !child {
			roots = append(roots, lifetime)
		}
	}
	service.mutex.Unlock()
	var closeErr error
	for index := len(roots) - 1; index >= 0; index-- {
		closeErr = errors.Join(
			closeErr,
			service.closeLifetime(context.Background(), roots[index]),
		)
	}
	service.mutex.Lock()
	service.deactivationErr = closeErr
	service.admission = registryInactive
	service.deactivation.close()
	service.mutex.Unlock()
	return closeErr
}
