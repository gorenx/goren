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
	if input.Config != nil {
		prepared, err := preparePrompt(*input.Config)
		if err != nil {
			gc.Response(Completion{
				Err: err,
			})
			return
		}
		layer := systemprompt.DeriveLayer(prepared.Snapshot)
		for _, section := range owner.sections {
			if _, err := layer.AddSection(gc.Context(), section); err != nil {
				gc.Response(Completion{
					Err: err,
				})
				return
			}
		}
		candidate.SetDefinitions(layer.Capture())
		owner.runtime = &candidate
		owner.layer = layer
		owner.settings = prepared.Config
		owner.registry = nil
		owner.version = 0
		gc.Response(Completion{})
		return
	}
	if owner.registry == nil {
		owner.runtime = &candidate
		gc.Response(Completion{})
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
	gc.RequestGrain(owner.registry, Prepare{}, goakt.WithRequestTimeout(goakt.DefaultGrainRequestTimeout)).Then(func(payload any, failure error) {
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
			owner.layer.SetBase(result.Snapshot)
			candidate.SetDefinitions(owner.layer.Capture())
			owner.runtime = &candidate
			owner.version = result.Version
			owner.settings = result.Config.Clone()
		}
		owner.configuration = nil
		retained.Response(Completion{
			Err: failure,
		})
	})
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
