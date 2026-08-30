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

// agentRecord is one exact in-process lifetime of a durable Session ID. It
// stores no parent or child object pointer; Registry owns those ID relations.
type agentRecord struct {
	id                  session.SessionID
	subject             Agent
	host                Host
	scope               Scope
	phase               recordPhase
	publication         publicationPhase
	descendantAdmission descendantAdmission
	construction        lifecycleSignal
	closing             lifecycleSignal
	closed              lifecycleSignal
	closeErr            error
}

// construction is one admitted Factory call. Registry does not retain this
// object, preventing a Registry-to-construction-to-Registry ownership cycle.
type construction struct {
	registry *RegistryService
	record   *agentRecord
	context  context.Context
	cancel   context.CancelCauseFunc
	done     chan struct{}
	once     sync.Once
}

func (admitted *construction) finish() {
	if admitted == nil || admitted.registry == nil {
		return
	}
	admitted.once.Do(func() {
		admitted.cancel(nil)
		<-admitted.done
		admitted.registry.mutex.Lock()
		delete(admitted.registry.constructions, admitted.record)
		admitted.registry.mutex.Unlock()
	})
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
	if service.admission != registryAccepting {
		return nil, nil, errors.New("agent: Agent Registry is shutting down")
	}
	if service.factory == nil {
		return nil, nil, errors.New("agent: Agent Registry is not active")
	}
	if _, exists := service.byID[identifier]; exists {
		return nil, nil, fmt.Errorf("agent: Agent %q is already active", identifier)
	}
	if parent != nil {
		parentRecord := service.byID[parent.ID()]
		if parentRecord == nil || !Same(parentRecord.subject, parent) {
			return nil, nil, errors.New(
				"agent: runtime parent is not an exact Agent in this Registry",
			)
		}
		if !recordVisible(parentRecord) ||
			parentRecord.descendantAdmission != descendantsAccepted {
			return nil, nil, ErrDescendantAdmissionClosed
		}
	}
	record := &agentRecord{
		id:                  identifier,
		phase:               recordConstructing,
		publication:         publicationUnpublished,
		descendantAdmission: descendantsAccepted,
		construction:        newLifecycleSignal(),
		closing:             newLifecycleSignal(),
		closed:              newLifecycleSignal(),
	}
	service.byID[identifier] = record
	service.records = append(service.records, record)
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
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-record.closing.done:
			cancelOperation(errors.New("agent: Agent construction is closing"))
		case <-operationContext.Done():
		}
	}()
	service.constructions[record] = struct{}{}
	return &construction{
		registry: service,
		record:   record,
		context:  operationContext,
		cancel:   cancelOperation,
		done:     done,
	}, service.factory, nil
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
	defer admitted.finish()
	agentHost, err := build(admitted.context, agentFactory)
	if err == nil {
		err = service.attach(admitted.record, agentHost)
	}
	if err == nil && contribution != nil {
		_, err = admitted.record.scope.ApplySetup(
			admitted.context,
			admitted.record.subject,
			contribution,
		)
	}
	if err == nil {
		err = agentHost.EnterServing(admitted.context)
	}
	if err == nil {
		err = agentHost.Announce(admitted.context)
	}
	if err == nil {
		err = service.publish(admitted.context, admitted.record, startSource)
	}
	if err != nil {
		admitted.record.construction.close()
		closeErr := service.closeRecord(
			context.WithoutCancel(requestContext),
			admitted.record,
		)
		if agentHost != nil && admitted.record.host == nil {
			closeErr = errors.Join(
				closeErr,
				agentHost.Close(context.WithoutCancel(requestContext)),
			)
			if constructedScope := agentHost.Scope(); constructedScope != nil {
				closeErr = errors.Join(
					closeErr,
					constructedScope.Close(context.WithoutCancel(requestContext)),
				)
			}
		}
		return Handle{}, errors.Join(err, closeErr)
	}
	return Handle{
		Subject:  admitted.record.subject,
		registry: service,
		record:   admitted.record,
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
			return agentFactory.CreateAgent(buildContext, creation)
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
			return agentFactory.ResumeAgent(buildContext, restoration)
		},
	)
}

func (service *RegistryService) attach(record *agentRecord, agentHost Host) error {
	if agentHost == nil || agentHost.Agent() == nil || agentHost.Scope() == nil {
		return errors.New("agent: Factory returned an incomplete Agent Host")
	}
	if agentHost.Agent().ID() != record.id {
		return fmt.Errorf(
			"agent: Agent id %q does not match reserved id %q",
			agentHost.Agent().ID(),
			record.id,
		)
	}
	service.mutex.Lock()
	defer service.mutex.Unlock()
	if record.phase != recordConstructing {
		return errors.New("agent: Agent construction is no longer active")
	}
	record.host = agentHost
	record.subject = agentHost.Agent()
	record.scope = agentHost.Scope()
	record.phase = recordAttached
	return nil
}

func (service *RegistryService) publish(
	requestContext context.Context,
	record *agentRecord,
	startSource SessionStartSource,
) error {
	service.mutex.Lock()
	if record.phase != recordAttached ||
		record.publication != publicationUnpublished {
		service.mutex.Unlock()
		return errors.New("agent: Agent is not ready for publication")
	}
	record.publication = publicationPublishing
	service.mutex.Unlock()
	err := record.scope.Dispatch(
		requestContext,
		Created{
			Subject: record.subject,
		},
	)
	service.mutex.Lock()
	record.publication = publicationPublished
	service.mutex.Unlock()
	if err != nil {
		return err
	}
	if observerErr := record.scope.Dispatch(
		requestContext,
		SessionStarted{
			Subject: record.subject,
			Source:  startSource,
		},
	); observerErr != nil {
		service.reportObserverFailure(fmt.Errorf(
			"agent: Agent %q session-start observer: %w",
			record.id,
			observerErr,
		))
	}
	if err = requestContext.Err(); err != nil {
		return err
	}
	service.mutex.Lock()
	defer service.mutex.Unlock()
	if record.phase != recordAttached {
		return errors.New("agent: Agent publication was interrupted")
	}
	record.phase = recordLive
	record.construction.close()
	return nil
}

func recordVisible(record *agentRecord) bool {
	if record == nil || record.subject == nil {
		return false
	}
	if record.phase == recordLive {
		return true
	}
	return record.phase == recordAttached &&
		(record.publication == publicationPublishing ||
			record.publication == publicationPublished)
}

func (service *RegistryService) closeRecord(
	closeContext context.Context,
	record *agentRecord,
) error {
	if closeContext == nil {
		closeContext = context.Background()
	}
	service.mutex.Lock()
	switch record.phase {
	case recordClosed:
		closeErr := record.closeErr
		service.mutex.Unlock()
		return closeErr
	case recordClosing:
		closed := record.closed.done
		service.mutex.Unlock()
		select {
		case <-closed:
			return record.closeErr
		case <-closeContext.Done():
			return context.Cause(closeContext)
		}
	default:
		record.phase = recordClosing
		record.descendantAdmission = descendantsClosing
		record.closing.close()
	}
	constructionDone := record.construction.done
	service.mutex.Unlock()
	completionContext := context.WithoutCancel(closeContext)
	<-constructionDone
	childRecords := service.children(record.id)
	var closeErr error
	for index := len(childRecords) - 1; index >= 0; index-- {
		closeErr = errors.Join(
			closeErr,
			service.closeRecord(completionContext, childRecords[index]),
		)
	}
	service.retirePublication(completionContext, record)
	if record.host != nil {
		closeErr = errors.Join(closeErr, record.host.Close(completionContext))
	}
	if record.scope != nil {
		closeErr = errors.Join(closeErr, record.scope.Close(completionContext))
	}
	service.finishClosed(record, closeErr)
	return closeErr
}

func (service *RegistryService) children(
	parentID session.SessionID,
) []*agentRecord {
	service.mutex.Lock()
	defer service.mutex.Unlock()
	result := make([]*agentRecord, 0)
	for _, candidate := range service.records {
		if service.parentByChild[candidate.id] == parentID &&
			candidate.phase != recordClosed {
			result = append(result, candidate)
		}
	}
	return result
}

func (service *RegistryService) retirePublication(
	requestContext context.Context,
	record *agentRecord,
) {
	service.mutex.Lock()
	if record.publication != publicationPublished || record.scope == nil {
		service.mutex.Unlock()
		return
	}
	record.publication = publicationRetired
	service.mutex.Unlock()
	if err := record.scope.Dispatch(
		requestContext,
		Disposed{
			Subject: record.subject,
		},
	); err != nil {
		service.reportObserverFailure(fmt.Errorf(
			"agent: Agent %q disposed observer: %w",
			record.id,
			err,
		))
	}
}

func (service *RegistryService) finishClosed(
	record *agentRecord,
	closeErr error,
) {
	service.mutex.Lock()
	defer service.mutex.Unlock()
	if record.phase == recordClosed {
		return
	}
	record.construction.close()
	record.closing.close()
	record.phase = recordClosed
	record.closeErr = closeErr
	if parentID, exists := service.parentByChild[record.id]; exists {
		delete(service.parentByChild, record.id)
		delete(service.childrenByParent[parentID], record.id)
		if len(service.childrenByParent[parentID]) == 0 {
			delete(service.childrenByParent, parentID)
		}
	}
	delete(service.childrenByParent, record.id)
	if service.byID[record.id] == record {
		delete(service.byID, record.id)
		service.records = slices.DeleteFunc(
			service.records,
			func(candidate *agentRecord) bool {
				return candidate == record
			},
		)
	}
	record.closed.close()
}

func (service *RegistryService) closeRegistry(closeContext context.Context) error {
	if closeContext == nil {
		closeContext = context.Background()
	}
	service.mutex.Lock()
	switch service.admission {
	case registryClosed:
		closeErr := service.shutdownErr
		service.mutex.Unlock()
		return closeErr
	case registryDraining:
		done := service.shutdown.done
		service.mutex.Unlock()
		select {
		case <-done:
			return service.shutdownErr
		case <-closeContext.Done():
			return context.Cause(closeContext)
		}
	case registryAccepting:
		service.admission = registryDraining
		service.factory = nil
	}
	for record := range service.constructions {
		record.closing.close()
	}
	roots := make([]*agentRecord, 0)
	for _, record := range service.records {
		if _, child := service.parentByChild[record.id]; !child {
			roots = append(roots, record)
		}
	}
	service.mutex.Unlock()
	var closeErr error
	for index := len(roots) - 1; index >= 0; index-- {
		closeErr = errors.Join(
			closeErr,
			service.closeRecord(context.WithoutCancel(closeContext), roots[index]),
		)
	}
	service.mutex.Lock()
	service.shutdownErr = closeErr
	service.admission = registryClosed
	service.shutdown.close()
	service.mutex.Unlock()
	return closeErr
}

// Shutdown rejects construction and disposes every Agent child-first.
func (service *RegistryService) Shutdown(closeContext context.Context) error {
	return service.closeRegistry(closeContext)
}
