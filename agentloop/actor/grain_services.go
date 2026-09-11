package loopactor

import (
	"errors"

	"github.com/google/uuid"
	"github.com/gorenx/goren/agent"
	middlewareactor "github.com/gorenx/goren/agent/middleware/actor"
	"github.com/gorenx/goren/agentloop"
	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	promptactor "github.com/gorenx/goren/systemprompt/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

// executePending is an Inbox wakeup, not an instruction to enter a Turn or Step.
type executePending struct{ creationID string }

// capabilityDelivery delivers the result to the receiver chosen by its caller.
// It carries no business phase or instruction for selecting the next operation.
type capabilityDelivery struct {
	creationID string
	id         string
	accept     func(agentloop.Services)
}

// serviceScope exists only during one Grain receive call. Delayed callbacks
// capture detached data and receivers, never this scope or its GrainContext.
type serviceScope struct {
	owner *Grain
	gc    *goakt.GrainContext
}

func (owner *Grain) services(gc *goakt.GrainContext) agentloop.Services {
	scope := serviceScope{
		owner: owner,
		gc:    gc,
	}
	return agentloop.Services{
		Conversation: sessionPort{scope: scope},
		Prompts:      promptPort{scope: scope},
		Middleware:   middlewarePort{scope: scope},
		Models:       modelPort{scope: scope},
		Tools:        toolPort{scope: scope},
		Observer:     scope,
	}
}

func (owner *Grain) receiveCapabilities(gc *goakt.GrainContext) bool {
	switch input := gc.Message().(type) {
	case executePending:
		if owner.checkCreation(input.creationID) == nil {
			err := owner.business.Run(owner.services(gc))
			if err != nil {
				owner.services(gc).Observer.ExecutionFailed(err)
			}
		}
	case capabilityDelivery:
		if owner.checkCreation(input.creationID) != nil {
			return true
		}
		owner.deliveries--
		delete(owner.cancellations, input.id)
		input.accept(owner.services(gc))
	default:
		return false
	}
	return true
}

// deliver normalizes immediate adapter errors and mailbox replies to a fresh
// receive call. Only detached receivers and result data cross this boundary.
func (scope serviceScope) deliver(accept func(agentloop.Services)) error {
	delivery := capabilityDelivery{
		creationID: scope.owner.creationID,
		accept:     accept,
	}
	err := scope.gc.PipeToSelf(func() (any, error) { return delivery, nil })
	if err == nil {
		scope.owner.deliveries++
	}
	return err
}

func (scope serviceScope) request(target *goakt.GrainIdentity, message any, cancellation *cancellationRequest, mode reentrancy.Mode, decode func(any, error) func(agentloop.Services)) error {
	identifier := uuid.NewString()
	creationID := scope.owner.creationID
	stopped := scope.owner.stopped
	completed := make(chan capabilityDelivery, 1)
	err := scope.gc.PipeToSelf(
		func() (any, error) {
			select {
			case result := <-completed:
				return result, nil
			case <-stopped:
				return nil, nil
			}
		},
	)
	if err != nil {
		return err
	}
	scope.owner.deliveries++
	if cancellation != nil {
		if scope.owner.cancellations == nil {
			scope.owner.cancellations = make(map[string]cancellationRequest)
		}
		scope.owner.cancellations[identifier] = *cancellation
	}
	scope.gc.RequestGrain(
		target, message,
		goakt.WithRequestTimeout(0),
		goakt.WithReentrancyMode(mode),
	).Then(
		func(payload any, failure error) {
			completed <- capabilityDelivery{
				creationID: creationID,
				id:         identifier,
				accept:     decode(payload, failure),
			}
		},
	)
	return nil
}

func (scope serviceScope) ExecutionFailed(failure error) {
	scope.gc.ActorSystem().Logger().Error(failure)
	scope.publish(agent.ErrorTopic,
		agent.ErrorNotice{
			AgentID: scope.owner.business.ID(),
			Err:     failure,
		},
	)
}

func (scope serviceScope) ExecutionFinished() { scope.owner.refreshIdle() }

func (scope serviceScope) publish(topic string, payload any) {
	target := scope.gc.ActorSystem().TopicActor()
	if target == nil {
		return
	}
	err := goakt.Tell(
		scope.gc.Context(),
		target,
		goakt.NewPublish(uuid.NewString(), topic, payload),
	)
	if err != nil {
		scope.gc.ActorSystem().Logger().Error(err)
	}
}

type sessionPort struct{ scope serviceScope }

func (endpoint sessionPort) Commit(plan session.WritePlan, receiver agentloop.CommitReceiver) error {
	return endpoint.scope.request(endpoint.scope.owner.conversation, sessionactor.Commit{
		CreationID: endpoint.scope.owner.creationID,
		Plan:       plan,
	}, nil, reentrancy.StashNonReentrant, func(payload any, failure error) func(agentloop.Services) {
		result, valid := payload.(sessionactor.Committed)
		if failure == nil {
			if !valid {
				failure = errors.New("agentloop: invalid Session commit response")
			} else {
				failure = result.Err
			}
		}
		return func(services agentloop.Services) { receiver.SessionCommitted(services, result.Result, failure) }
	})
}

func (endpoint sessionPort) Read(receiver agentloop.SnapshotReceiver) error {
	return endpoint.scope.request(endpoint.scope.owner.conversation, sessionactor.Read{
		CreationID: endpoint.scope.owner.creationID,
	}, nil, reentrancy.StashNonReentrant, func(payload any, failure error) func(agentloop.Services) {
		result, valid := payload.(sessionactor.View)
		if failure == nil {
			if !valid {
				failure = errors.New("agentloop: invalid Session read response")
			} else {
				failure = result.Err
			}
		}
		return func(services agentloop.Services) { receiver.SessionRead(services, result.Snapshot, failure) }
	})
}

func (endpoint sessionPort) Flush(barrier session.WriteBarrier, receiver agentloop.FlushReceiver) error {
	return endpoint.scope.request(endpoint.scope.owner.conversation, sessionactor.Flush{
		CreationID: endpoint.scope.owner.creationID,
		Barrier:    barrier,
	}, nil, reentrancy.StashNonReentrant, func(payload any, failure error) func(agentloop.Services) {
		result, valid := payload.(sessionactor.Flushed)
		if failure == nil {
			if !valid {
				failure = errors.New("agentloop: invalid Session flush response")
			} else {
				failure = result.Err
			}
		}
		return func(services agentloop.Services) { receiver.SessionFlushed(services, result.Barrier, failure) }
	})
}

type promptPort struct{ scope serviceScope }

func (endpoint promptPort) Assemble(receiver agentloop.PromptReceiver) error {
	identifier := uuid.NewString()
	target := endpoint.scope.owner.prompt
	return endpoint.scope.request(
		target,
		promptactor.Assemble{ID: identifier},
		&cancellationRequest{
			target:  target,
			message: promptactor.CancelAssembly{ID: identifier},
		},
		reentrancy.AllowAll,
		func(payload any, failure error) func(agentloop.Services) {
			result, valid := payload.(promptactor.Assembled)
			if failure == nil {
				if !valid || result.ID != identifier {
					failure = errors.New("agentloop: invalid Prompt response")
				} else {
					failure = result.Err
				}
			}
			return func(services agentloop.Services) { receiver.PromptAssembled(services, result.Prompt, failure) }
		},
	)
}

type middlewarePort struct{ scope serviceScope }

func (endpoint middlewarePort) PreStep(input agentloop.PreStep, receiver agentloop.PreStepReceiver) error {
	identifier := middlewareactor.CallID(uuid.NewString())
	return endpoint.invoke(
		identifier,
		middlewareactor.ResolvePreStep{
			Routes:  endpoint.scope.owner.handlers.PreStep,
			CallID:  identifier,
			Input:   input.Input,
			Default: input.Default,
		},
		func(payload any, failure error) func(agentloop.Services) {
			result, valid := payload.(agent.PreStepResolved)
			if failure != nil {
				result.Err = failure
			} else if !valid {
				result.Err = errors.New("agentloop: invalid PreStep response")
			}
			return func(services agentloop.Services) { receiver.PreStepResolved(services, result) }
		},
	)
}

func (endpoint middlewarePort) TurnStopping(input agent.TurnStoppingInput, receiver agentloop.StoppingReceiver) error {
	identifier := middlewareactor.CallID(uuid.NewString())
	return endpoint.invoke(
		identifier,
		middlewareactor.RunTurnStopping{
			Routes: endpoint.scope.owner.handlers.TurnStopping,
			CallID: identifier,
			Input:  input,
		},
		func(payload any, failure error) func(agentloop.Services) {
			result, valid := payload.(agent.TurnStoppingCompleted)
			if failure != nil {
				result.Err = failure
			} else if !valid {
				result.Err = errors.New("agentloop: invalid stopping response")
			}
			return func(services agentloop.Services) { receiver.TurnStoppingCompleted(services, result) }
		},
	)
}

func (endpoint middlewarePort) RequestError(input agent.RequestFailure, receiver agentloop.RecoveryReceiver) error {
	identifier := middlewareactor.CallID(uuid.NewString())
	return endpoint.invoke(identifier, middlewareactor.ResolveRequestError{
		Routes: endpoint.scope.owner.handlers.RequestError,
		CallID: identifier,
		Input:  input,
	}, func(payload any, failure error) func(agentloop.Services) {
		result, valid := payload.(agent.RequestErrorResolved)
		if failure != nil {
			result.Err = failure
		} else if !valid {
			result.Err = errors.New("agentloop: invalid recovery response")
		}
		return func(services agentloop.Services) { receiver.RequestErrorResolved(services, result) }
	})
}

func (endpoint middlewarePort) invoke(identifier middlewareactor.CallID, message any, decode func(any, error) func(agentloop.Services)) error {
	target := endpoint.scope.owner.middleware
	return endpoint.scope.request(target, message, &cancellationRequest{
		target:  target,
		message: middlewareactor.CancelCall{CallID: identifier},
	}, reentrancy.AllowAll, decode)
}
