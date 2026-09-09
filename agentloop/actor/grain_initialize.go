package loopactor

import (
	"context"
	"errors"

	agentactor "github.com/gorenx/goren/agent/actor"
	sessionactor "github.com/gorenx/goren/session/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

type initialization struct {
	input  agentactor.Initialize
	reply  *goakt.GrainReply
	cancel context.CancelFunc
}

type initializationAuthorized struct {
	operation *initialization
	err       error
}

func (owner *Grain) initialize(gc *goakt.GrainContext, input agentactor.Initialize) {
	if owner.creationID != "" ||
		owner.initialization != nil ||
		input.Authority == nil ||
		input.AgentID == "" ||
		gc.Self().Name() != RuntimeName(string(input.AgentID)) ||
		input.Session == nil ||
		input.Prompt == nil ||
		input.CreationID == "" {
		gc.Response(agentactor.Initialized{
			Err: errors.New("agentloop: matching uninitialized Agent, Session and authority are required"),
		})
		return
	}
	if err := gc.EnableReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.StashNonReentrant))); err != nil {
		gc.Response(agentactor.Initialized{
			Err: err,
		})
		return
	}
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(agentactor.Initialized{
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
		AgentID:    input.AgentID,
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

func (owner *Grain) restoreInbox(gc *goakt.GrainContext, input initializationAuthorized) {
	operation := input.operation
	if owner.initialization != operation {
		return
	}
	if input.err != nil {
		owner.finishInitialization(operation, input.err)
		return
	}
	owner.creationID = operation.input.CreationID
	owner.conversation = operation.input.Session
	owner.prompt = operation.input.Prompt
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
		CreationID: owner.creationID,
	}, goakt.WithRequestTimeout(0)).Then(func(payload any, failure error) {
		if owner.initialization != operation {
			return
		}
		if failure == nil {
			view, ok := payload.(sessionactor.View)
			switch {
			case !ok:
				failure = errors.New("agentloop: invalid Session read response")
			case view.Err != nil:
				failure = view.Err
			case view.Header.ID != operation.input.AgentID || view.Snapshot.Barrier.SessionID != view.Header.ID:
				failure = errors.New("agentloop: Session identity mismatch")
			default:
				failure = owner.business.Restore(view.Header, view.Snapshot)
			}
		}
		completed <- initializationRestored{
			operation: operation,
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
		failure = owner.business.Initialize()
	}
	operation.cancel()
	operation.reply.Response(agentactor.Initialized{
		Err: failure,
	})
}
