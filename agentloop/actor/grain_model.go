package loopactor

import (
	"context"
	"errors"

	"github.com/gorenx/goren/llm"
	configactor "github.com/gorenx/goren/llm/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

type initializationRestored struct {
	operation *initialization
	overrides llm.CallConfig
	err       error
}

func (owner *Grain) initializeModel(gc *goakt.GrainContext, input initializationRestored) {
	operation := input.operation
	if owner.initialization != operation {
		return
	}
	if input.err != nil {
		owner.finishInitialization(operation, input.err)
		return
	}

	operation.cancel()
	ctx, cancel := context.WithCancel(context.WithoutCancel(gc.Context()))
	operation.cancel = cancel
	completed := make(chan initializationModel, 1)
	if err := gc.PipeToSelf(func() (any, error) {
		select {
		case result := <-completed:
			return result, nil
		case <-ctx.Done():
			return initializationModel{
				operation: operation,
				err:       context.Cause(ctx),
			}, nil
		}
	}); err != nil {
		owner.finishInitialization(operation, err)
		return
	}
	gc.RequestGrain(
		operation.input.ModelConfig,
		configactor.ResolveAgent{
			Overrides: input.overrides,
		},
		goakt.WithRequestTimeout(0),
	).Then(
		func(payload any, failure error) {
			if owner.initialization != operation {
				return
			}
			if failure == nil {
				result, ok := payload.(configactor.AgentConfiguration)
				switch {
				case !ok:
					failure = errors.New("agentloop: invalid model configuration response")
				case result.Err != nil:
					failure = result.Err
				default:
					operation.configuration = result.Resolved
				}
			}
			completed <- initializationModel{
				operation: operation,
				err:       failure,
			}
		},
	)
}
