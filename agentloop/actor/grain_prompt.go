package loopactor

import (
	"errors"

	promptactor "github.com/gorenx/goren/systemprompt/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

type initializationRestored struct {
	operation *initialization
	err       error
}

func (owner *Grain) initializePrompt(gc *goakt.GrainContext, input initializationRestored) {
	operation := input.operation
	if owner.initialization != operation {
		return
	}
	if input.err != nil {
		owner.finishInitialization(operation, input.err)
		return
	}
	gc.RequestGrain(
		owner.prompt, promptactor.ReadRuntime{},
		goakt.WithRequestTimeout(0),
	).Then(
		func(payload any, failure error) {
			if owner.initialization != operation {
				return
			}
			if failure == nil {
				result, ok := payload.(promptactor.RuntimeView)
				if !ok {
					failure = errors.New("agentloop: invalid Prompt view")
				} else {
					failure = result.Err
				}
			}
			owner.finishInitialization(operation, failure)
		})
}
