package agentloop

import (
	"context"
	"errors"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
	sesspersist "github.com/gorenx/goren/session/persistence"
)

// Factory owns the Agent Loop construction use cases. It is a plain business
// service registered behind agent.Factory, not a Plugin.
type Factory struct {
	maxParallelToolCalls int
	sessions             session.LiveStore
	persistence          sesspersist.Persistence
	failures             observerFailureReporter
	runtimeContextEvents *runtimeContextRouter
	scopes               scopeHost
}

func newFactory(
	maxParallelToolCalls int,
	sessions session.LiveStore,
	persistence sesspersist.Persistence,
	failures observerFailureReporter,
	runtimeContextEvents *runtimeContextRouter,
	scopes scopeHost,
) *Factory {
	return &Factory{
		maxParallelToolCalls: maxParallelToolCalls,
		sessions:             sessions,
		persistence:          persistence,
		failures:             failures,
		runtimeContextEvents: runtimeContextEvents,
		scopes:               scopes,
	}
}

func (owner *Factory) CreateAgent(
	requestContext context.Context,
	reservation agent.Reservation,
	options agent.CreateOptions,
) error {
	operationContext, finishConstruction, err := owner.begin(
		requestContext,
		reservation,
	)
	if err != nil {
		return err
	}
	defer finishConstruction()
	if options.SessionID == "" {
		return errors.New("agentloop: Agent Session id is empty")
	}
	if err = validateAgentOptions(options.AgentOptions); err != nil {
		return err
	}
	identifier := options.SessionID
	conversation, err := owner.sessions.Prepare(
		&identifier,
		session.CreateOptions{
			Seed:     options.Seed,
			Metadata: options.Metadata,
		},
	)
	if err != nil {
		return err
	}
	prepared, err := newPreparedAgent(
		conversation,
		options.AgentOptions,
		owner.maxParallelToolCalls,
		owner.failures,
		owner.runtimeContextEvents,
		owner.scopes,
	)
	if err != nil {
		return err
	}
	return prepared.publish(
		operationContext,
		reservation,
		options.Provisioner,
	)
}

func (owner *Factory) ResumeAgent(
	requestContext context.Context,
	reservation agent.Reservation,
	options agent.ResumeOptions,
) error {
	operationContext, finishConstruction, err := owner.begin(
		requestContext,
		reservation,
	)
	if err != nil {
		return err
	}
	defer finishConstruction()
	if options.SessionID == "" {
		return errors.New("agentloop: resume Session id is empty")
	}
	if err = validateAgentOptions(options.AgentOptions); err != nil {
		return err
	}
	if owner.persistence == nil {
		return errors.New("agentloop: session persistence is not configured")
	}
	preparation, err := owner.persistence.Prepare(
		operationContext,
		options.SessionID,
	)
	if err != nil {
		return err
	}
	defer preparation.Dispose()
	prepared, err := newPreparedAgent(
		preparation.UnpublishedSession(),
		options.AgentOptions,
		owner.maxParallelToolCalls,
		owner.failures,
		owner.runtimeContextEvents,
		owner.scopes,
	)
	if err != nil {
		return err
	}
	return prepared.publish(
		operationContext,
		reservation,
		options.Provisioner,
	)
}

func (owner *Factory) begin(
	requestContext context.Context,
	reservation agent.Reservation,
) (context.Context, func(), error) {
	if reservation == nil {
		return nil, nil, errors.New("agentloop: Agent Reservation is required")
	}
	if requestContext == nil {
		return nil, nil, errors.New("agentloop: construction Context is nil")
	}
	operationContext, cancelOperation := context.WithCancelCause(requestContext)
	followedContext, cancelFollowed := context.WithCancelCause(operationContext)
	followingDone := make(chan struct{})
	go func() {
		defer close(followingDone)
		select {
		case <-reservation.ClosingSignal():
			cancelFollowed(errors.New("agentloop: Agent construction is closing"))
		case <-followedContext.Done():
		}
	}()
	var completeOnce sync.Once
	complete := func() {
		completeOnce.Do(func() {
			cancelFollowed(nil)
			<-followingDone
			cancelOperation(nil)
		})
	}
	if err := contextFailure(followedContext); err != nil {
		complete()
		return nil, nil, err
	}
	return followedContext, complete, nil
}

var _ agent.Factory = (*Factory)(nil)
