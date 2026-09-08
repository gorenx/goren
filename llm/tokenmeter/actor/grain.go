package meteractor

import (
	"context"
	"errors"

	"github.com/gorenx/goren/llm/tokenmeter"
	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Grain serializes pure measurement for the SessionID used as its framework name.
// Composition keeps it alive while that Session's creation records are needed.
type Grain struct {
	// meters maps a CreationID to its independent single-log fold. A present nil
	// entry remembers closure within this activation and rejects late requests.
	meters map[string]*tokenmeter.TokenMeter
}

// OnActivate allocates rebuildable creation-local measurement state.
func (owner *Grain) OnActivate(context.Context, *goakt.GrainProps) error {
	owner.meters = make(map[string]*tokenmeter.TokenMeter)
	return nil
}

// OnDeactivate drops memory without closing Session or shared storage.
func (owner *Grain) OnDeactivate(context.Context, *goakt.GrainProps) error {
	owner.meters = nil
	return nil
}

// OnReceive completes pure computations directly; callers use RequestGrain.
func (owner *Grain) OnReceive(gc *goakt.GrainContext) {
	switch input := gc.Message().(type) {
	case Measure:
		if err := validateCreation(gc.Self().Name(), input.SessionID, input.CreationID); err != nil {
			gc.Response(Measured{
				Err: err,
			})
			return
		}
		pricing, found := owner.meters[input.CreationID]
		if found && pricing == nil {
			gc.Response(Measured{
				Err: errors.New("tokenmeter: Session creation is closed"),
			})
			return
		}
		if !found {
			pricing = new(tokenmeter.TokenMeter)
		}
		value, err := pricing.Measure(gc.Context(), input.Events, input.Header)
		if err == nil {
			owner.meters[input.CreationID] = pricing
		}
		gc.Response(Measured{
			Value: value,
			Err:   err,
		})
	case EstimateMessage:
		pricing := tokenmeter.TokenMeter{}
		tokens, err := pricing.EstimateMessage(input.Message)
		gc.Response(Estimated{
			Tokens: tokens,
			Err:    err,
		})
	case Close:
		gc.Response(Completion{
			Err: owner.close(gc.Self().Name(), input.SessionID, input.CreationID),
		})
	case sessionactor.ClosedNotice:
		gc.Response(Completion{
			Err: owner.close(gc.Self().Name(), input.SessionID, input.CreationID),
		})
	default:
		gc.Unhandled()
	}
}

func (owner *Grain) close(name string, identifier session.SessionID, creationID string) error {
	if err := validateCreation(name, identifier, creationID); err != nil {
		return err
	}
	owner.meters[creationID] = nil
	return nil
}

func validateCreation(name string, identifier session.SessionID, creationID string) error {
	if identifier == "" || RuntimeName(string(identifier)) != name || creationID == "" {
		return errors.New("tokenmeter: matching Session and non-empty creation identity are required")
	}
	return nil
}
