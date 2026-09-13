package promptactor

import (
	"errors"

	"github.com/gorenx/goren/systemprompt"

	goakt "github.com/tochemey/goakt/v4/actor"
)

func (owner *RuntimeGrain) applyConfiguration(gc *goakt.GrainContext, input ApplyConfiguration) {
	if owner.runtime == nil ||
		owner.closed ||
		owner.closing != nil {
		gc.Response(Completion{
			Err: ErrUnavailable,
		})
		return
	}
	if owner.configuration != nil {
		gc.Response(Completion{
			Err: ErrBusy,
		})
		return
	}
	candidate := *owner.runtime
	if err := candidate.ApplyTools(input.AgentID, input.Definitions); err != nil {
		gc.Response(Completion{
			Err: err,
		})
		return
	}
	if owner.registry == nil {
		gc.Response(Completion{
			Err: owner.adoptConfiguration(candidate, owner.layer),
		})
		return
	}
	retained := gc.DeferResponse()
	if retained == nil {
		gc.Response(Completion{
			Err: ErrAsyncRequest,
		})
		return
	}
	owner.configuration = retained
	gc.RequestGrain(
		owner.registry,
		Prepare{},
		goakt.WithRequestTimeout(goakt.DefaultGrainRequestTimeout),
	).Then(
		func(payload any, failure error) {
			if owner.configuration != retained {
				return
			}
			result, valid := payload.(Prepared)
			if failure == nil {
				switch {
				case !valid:
					failure = errors.New("systemprompt: invalid registration snapshot response")
				case result.Err != nil:
					failure = result.Err
				case result.Version < owner.version:
					failure = errors.New("systemprompt: stale registration snapshot")
				}
			}
			if failure == nil {
				layer := *owner.layer
				layer.SetBase(result.Snapshot)
				failure = owner.adoptConfiguration(candidate, &layer)
				if failure == nil {
					owner.version = result.Version
				}
			}
			owner.configuration = nil
			retained.Response(Completion{
				Err: failure,
			})
		})
}

// adoptConfiguration installs one complete projection without changing its
// definition source or registration ownership. All validation precedes mutation.
func (owner *RuntimeGrain) adoptConfiguration(
	candidate systemprompt.Runtime,
	layer *systemprompt.PromptLayer,
) error {
	captured, err := layer.Capture().WithConfiguration(owner.settings)
	if err != nil {
		return err
	}
	candidate.SetDefinitions(captured)
	owner.runtime = &candidate
	owner.layer = layer
	return nil
}

// preparePrompt uses the same owner validation and built-in definitions as the
// process baseline, without registering another shared owner or retaining it.
func preparePrompt(settings systemprompt.Config) (Prepared, error) {
	validated, err := systemprompt.ValidateConfig(settings)
	if err != nil {
		return Prepared{}, err
	}
	layer, err := systemprompt.NewLayer(validated)
	if err != nil {
		return Prepared{}, err
	}
	return Prepared{
		Config:   layer.Configuration(),
		Snapshot: layer.Capture(),
	}, nil
}
