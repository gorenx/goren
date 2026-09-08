package loopactor

import (
	"context"
	"errors"

	agentactor "github.com/gorenx/goren/agent/actor"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/llm/modelconfig"
	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	sessionregistry "github.com/gorenx/goren/session/registry"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

type initialization struct {
	input         Initialize
	reply         *goakt.GrainReply
	cancel        context.CancelFunc
	configuration modelconfig.Resolved
	toolsVersion  uint64
}

type initializationAuthorized struct {
	operation *initialization
	err       error
}

func (owner *Grain) initialize(gc *goakt.GrainContext, input Initialize) {
	if owner.creationID != "" || owner.initialization != nil || input.Authority == nil || input.SessionRegistry == nil || input.ModelConfig == nil || input.PromptRegistry == nil || input.ToolsRegistry == nil || input.CreationID == "" {
		gc.Response(Completion{
			Err: errors.New("agentloop: matching uninitialized Agent, Session and authority are required"),
		})
		return
	}
	if err := gc.EnableReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.StashNonReentrant))); err != nil {
		gc.Response(Completion{
			Err: err,
		})
		return
	}
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(Completion{
			Err: errors.New("agentloop: initialization requires an asynchronous request"),
		})
		return
	}
	ctx, cancel := context.WithCancel(gc.Context())
	operation := &initialization{
		input:  input,
		reply:  reply,
		cancel: cancel,
	}
	owner.initialization = operation
	authorized := make(chan error, 1)
	if err := gc.PipeToSelf(func() (any, error) {
		var failure error
		select {
		case failure = <-authorized:
		case <-ctx.Done():
			failure = context.Cause(ctx)
		}
		return initializationAuthorized{
			operation: operation,
			err:       failure,
		}, nil
	}); err != nil {
		owner.finishInitialization(operation, err)
		return
	}
	gc.RequestGrain(input.Authority, agentactor.Claim{
		AgentID:    session.SessionID(gc.Self().Name()),
		CreationID: input.CreationID,
		InstanceID: owner.instanceID,
		Executor:   gc.Self(),
	}, goakt.WithRequestTimeout(0)).Then(func(payload any, failure error) {
		if failure == nil {
			result, ok := payload.(agentactor.Claimed)
			if !ok {
				failure = errors.New("agentloop: invalid Registry claim response")
			} else {
				failure = result.Err
			}
		}
		authorized <- failure
	})
}

func (owner *Grain) startSession(gc *goakt.GrainContext, input initializationAuthorized) {
	operation := input.operation
	if owner.initialization != operation {
		return
	}
	if input.err != nil {
		owner.finishInitialization(operation, input.err)
		return
	}
	// Authorization assigns cleanup responsibility even if reading the Session
	// or restoring the Inbox fails. A missing Inbox is not proof of closed resources.
	owner.creationID = operation.input.CreationID
	owner.sessionRegistry = operation.input.SessionRegistry
	conversation, err := goakt.GrainOf[*sessionactor.Grain](gc.Context(), gc.ActorSystem(), gc.Self().Name(), goakt.WithLongLivedGrain())
	if err != nil {
		owner.finishInitialization(operation, err)
		return
	}
	owner.conversation = conversation

	operation.cancel()
	ctx, cancel := context.WithCancel(context.WithoutCancel(gc.Context()))
	operation.cancel = cancel
	completed := make(chan initializationSession, 1)
	if err := gc.PipeToSelf(func() (any, error) {
		select {
		case result := <-completed:
			return result, nil
		case <-ctx.Done():
			return initializationSession{
				operation: operation,
				err:       context.Cause(ctx),
			}, nil
		}
	}); err != nil {
		owner.finishInitialization(operation, err)
		return
	}
	gc.RequestGrain(owner.sessionRegistry, sessionregistry.StartSession{
		SessionID:  session.SessionID(gc.Self().Name()),
		CreationID: owner.creationID,
	}, goakt.WithRequestTimeout(0)).Then(func(payload any, failure error) {
		result, ok := payload.(sessionactor.Initialized)
		if failure == nil {
			if !ok {
				failure = errors.New("agentloop: invalid Session initialization response")
			} else {
				failure = result.Err
			}
		}
		completed <- initializationSession{
			operation: operation,
			result:    result,
			err:       failure,
		}
	})
}

type initializationSession struct {
	operation *initialization
	result    sessionactor.Initialized
	err       error
}

func (owner *Grain) restoreInbox(gc *goakt.GrainContext, input initializationSession) {
	operation := input.operation
	if owner.initialization != operation {
		return
	}
	if input.err != nil {
		owner.finishInitialization(operation, input.err)
		return
	}
	metadata := input.result.Header.Clone()
	if string(metadata.ID) != gc.Self().Name() || input.result.Barrier.SessionID != metadata.ID {
		owner.finishInitialization(operation, errors.New("agentloop: Session initialization identity mismatch"))
		return
	}

	operation.cancel()
	ctx, cancel := context.WithCancel(context.WithoutCancel(gc.Context()))
	operation.cancel = cancel
	completed := make(chan initializationRestored, 1)
	if err := gc.PipeToSelf(func() (any, error) {
		select {
		case result := <-completed:
			return result, nil
		case <-ctx.Done():
			return initializationRestored{
				operation: operation,
				err:       context.Cause(ctx),
			}, nil
		}
	}); err != nil {
		owner.finishInitialization(operation, err)
		return
	}
	gc.RequestGrain(owner.conversation, sessionactor.Read{
		CreationID: operation.input.CreationID,
	}, goakt.WithRequestTimeout(0)).Then(func(payload any, failure error) {
		if owner.initialization != operation {
			return
		}
		var overrides llm.CallConfig
		if failure == nil {
			view, ok := payload.(sessionactor.View)
			switch {
			case !ok:
				failure = errors.New("agentloop: invalid Session read response")
			case view.Err != nil:
				failure = view.Err
			default:
				overrides, failure = owner.business.Restore(metadata, view.Snapshot)
			}
		}
		completed <- initializationRestored{
			operation: operation,
			overrides: overrides,
			err:       failure,
		}
	})
}

func (owner *Grain) finishInitialization(operation *initialization, failure error) {
	defer owner.refreshIdle()
	if owner.initialization != operation {
		return
	}
	owner.initialization = nil
	if failure == nil {
		failure = owner.business.Initialize(operation.configuration, operation.toolsVersion)
	}
	operation.cancel()
	operation.reply.Response(Completion{
		Err: failure,
	})
}
