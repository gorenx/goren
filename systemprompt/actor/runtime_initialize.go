package promptactor

import (
	"context"
	"errors"

	"github.com/gorenx/goren/systemprompt"
	toolactor "github.com/gorenx/goren/tools/registry/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

type runtimeInitialization struct {
	input  InitializeRuntime
	reply  *goakt.GrainReply
	cancel context.CancelFunc
}

type promptPrepared struct {
	operation *runtimeInitialization
	result    Prepared
	err       error
}

func (owner *RuntimeGrain) initializeRuntime(gc *goakt.GrainContext, input InitializeRuntime) {
	if owner.runtime != nil || owner.initialization != nil ||
		owner.closed || input.AgentID == "" ||
		input.PromptRegistry == nil || input.ToolsRegistry == nil {
		gc.Response(RuntimeInitialized{
			Err: errors.New("systemprompt: uninitialized Runtime and registry routes are required"),
		})
		return
	}
	if err := gc.EnableReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.StashNonReentrant))); err != nil {
		gc.Response(RuntimeInitialized{
			Err: err,
		})
		return
	}
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(RuntimeInitialized{
			Err: ErrAsyncRequest,
		})
		return
	}
	ctx, cancel := context.WithCancel(gc.Context())
	operation := &runtimeInitialization{
		input:  input,
		reply:  reply,
		cancel: cancel,
	}
	owner.initialization = operation
	completed := make(chan promptPrepared, 1)
	if err := gc.PipeToSelf(func() (any, error) {
		select {
		case result := <-completed:
			return result, nil
		case <-ctx.Done():
			return promptPrepared{
				operation: operation,
				err:       context.Cause(ctx),
			}, nil
		}
	}); err != nil {
		owner.finishInitialization(operation, err)
		return
	}
	gc.RequestGrain(
		input.PromptRegistry, Prepare{},
		goakt.WithRequestTimeout(0)).Then(
		func(payload any, failure error) {
			result, ok := payload.(Prepared)
			if failure == nil {
				if !ok {
					failure = errors.New("systemprompt: invalid registration snapshot response")
				} else {
					failure = result.Err
				}
			}
			completed <- promptPrepared{
				operation: operation,
				result:    result,
				err:       failure,
			}
		},
	)
}

func (owner *RuntimeGrain) initializeTools(gc *goakt.GrainContext, input promptPrepared) {
	operation := input.operation
	if owner.initialization != operation {
		return
	}
	if input.err != nil {
		owner.finishInitialization(operation, input.err)
		return
	}
	candidate, err := systemprompt.NewRuntime(operation.input.AgentID, input.result.Snapshot)
	if err != nil {
		owner.finishInitialization(operation, err)
		return
	}
	gc.RequestGrain(
		operation.input.ToolsRegistry,
		toolactor.ReadDefinitions{
			AgentID: operation.input.AgentID,
		}, goakt.WithRequestTimeout(0)).Then(
		func(payload any, failure error) {
			if owner.initialization != operation {
				return
			}
			if failure == nil {
				result, ok := payload.(toolactor.Catalog)
				switch {
				case !ok:
					failure = errors.New("systemprompt: invalid tool baseline response")
				case result.Err != nil:
					failure = result.Err
				case !result.Found:
					failure = systemprompt.ErrToolsNotReady
				default:
					failure = candidate.ApplyTools(result.Definitions.AgentID, systemprompt.ToolDefinitions{
						Schemas:    result.Definitions.Schemas,
						KnownNames: result.Definitions.KnownNames,
					})
				}
			}
			if failure == nil {
				owner.runtime = candidate
			}
			owner.finishInitialization(operation, failure)
		},
	)
}

func (owner *RuntimeGrain) finishInitialization(operation *runtimeInitialization, failure error) {
	if owner.initialization != operation {
		return
	}
	owner.initialization = nil
	operation.cancel()
	operation.reply.Response(RuntimeInitialized{

		Err: failure,
	})
}
