package loopactor

import (
	"context"
	"errors"

	agentactor "github.com/gorenx/goren/agent/actor"
	"github.com/gorenx/goren/session"
	promptactor "github.com/gorenx/goren/systemprompt/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

type initializationModel struct {
	operation *initialization
	err       error
}

func (owner *Grain) initializePrompt(gc *goakt.GrainContext, input initializationModel) {
	operation := input.operation
	if owner.initialization != operation {
		return
	}
	if input.err != nil {
		owner.finishInitialization(operation, input.err)
		return
	}
	identity, err := goakt.GrainOf[*promptactor.RuntimeGrain](gc.Context(), gc.ActorSystem(), gc.Self().Name(), goakt.WithLongLivedGrain())
	if err != nil {
		owner.finishInitialization(operation, err)
		return
	}
	// Record cleanup responsibility before the component can acquire resources.
	owner.prompt = identity
	gc.RequestGrain(identity, promptactor.InitializeRuntime{
		AgentID:        session.SessionID(gc.Self().Name()),
		PromptRegistry: operation.input.PromptRegistry,
		ToolsRegistry:  operation.input.ToolsRegistry,
	}, goakt.WithRequestTimeout(0)).Then(func(payload any, failure error) {
		if owner.initialization != operation {
			return
		}
		if failure == nil {
			result, ok := payload.(promptactor.RuntimeInitialized)
			switch {
			case !ok:
				failure = errors.New("agentloop: invalid Prompt initialization response")
			case result.Err != nil:
				failure = result.Err
			default:
				operation.toolsVersion = result.ToolsVersion
			}
		}
		owner.finishInitialization(operation, failure)
	})
}

type promptClosure struct {
	reply  *goakt.GrainReply
	cancel context.CancelFunc
}

type promptStopped struct {
	operation *promptClosure
	err       error
}

func (owner *Grain) closePrompt(gc *goakt.GrainContext, reply *goakt.GrainReply) {
	system := gc.ActorSystem()
	identity := owner.prompt
	ctx, cancel := context.WithCancel(context.WithoutCancel(gc.Context()))
	operation := &promptClosure{
		reply:  reply,
		cancel: cancel,
	}
	owner.promptClosing = operation
	completed := make(chan error, 1)
	if err := gc.PipeToSelf(func() (any, error) {
		var failure error
		select {
		case failure = <-completed:
		case <-ctx.Done():
			failure = context.Cause(ctx)
		}
		if failure == nil {
			failure = system.TellGrain(ctx, identity, new(goakt.PoisonPill))
		}
		return promptStopped{
			operation: operation,
			err:       failure,
		}, nil
	}); err != nil {
		cancel()
		owner.promptClosing = nil
		reply.Response(agentactor.Closed{
			Err: err,
		})
		return
	}
	gc.RequestGrain(
		identity, promptactor.CloseRuntime{},
		goakt.WithRequestTimeout(0)).Then(
		func(payload any, failure error) {
			if failure == nil {
				result, ok := payload.(promptactor.Completion)
				if !ok {
					failure = errors.New("agentloop: invalid Prompt close response")
				} else {
					failure = result.Err
				}
			}
			completed <- failure
		},
	)
}

func (owner *Grain) finishPromptClose(gc *goakt.GrainContext, input promptStopped) {
	if owner.promptClosing != input.operation {
		return
	}
	input.operation.cancel()
	owner.promptClosing = nil
	if input.err != nil {
		input.operation.reply.Response(agentactor.Closed{
			Err: input.err,
		})
		return
	}
	owner.prompt = nil
	owner.closeSession(gc, input.operation.reply)
}
