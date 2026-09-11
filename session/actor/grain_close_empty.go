package sessionactor

import (
	"errors"

	"github.com/gorenx/goren/session"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

// closeEmpty confirms that a failed initialization has left no work to drain.
// It never treats absence of an in-memory log as absence of durable data.
func (owner *Grain) closeEmpty(gc *goakt.GrainContext, input Close) {
	switch {
	case input.SessionID == "" || gc.Self().Name() != RuntimeName(string(input.SessionID)) || input.CreationID == "":
		gc.Response(Closed{
			Err: ErrCreation,
		})
		return
	case owner.admission != nil || owner.restoring != nil:
		gc.Response(Closed{
			Err: ErrRestoring,
		})
		return
	case owner.closing != nil:
		if owner.creationID != input.CreationID {
			gc.Response(Closed{
				Err: ErrCreation,
			})
		} else {
			gc.Response(Closed{
				Barrier: owner.closing.barrier,
			})
		}
		return
	}
	if err := gc.EnableReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))); err != nil {
		gc.Response(Closed{
			Err: err,
		})
		return
	}
	if input.Authority == nil {
		gc.Response(Closed{
			Err: errors.New("session: creation authority is required"),
		})
		return
	}
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(Closed{
			Err: errors.New("session: empty close requires an asynchronous request"),
		})
		return
	}
	identifier := input.SessionID
	gc.RequestGrain(input.Authority, Claim{
		SessionID:  identifier,
		CreationID: input.CreationID,
		InstanceID: owner.instanceID,
	}).Then(func(payload any, failure error) {
		if failure == nil {
			result, ok := payload.(Claimed)
			if !ok {
				failure = errors.New("session: invalid close authorization reply")
			} else {
				failure = result.Err
			}
		}
		if failure == nil {
			switch {
			case owner.admission != nil || owner.restoring != nil:
				failure = ErrRestoring
			case owner.history != nil:
				failure = errors.New("session: initialization completed during close authorization; retry close")
			}
		}
		if failure != nil {
			reply.Response(Closed{
				Err: failure,
			})
			return
		}
		owner.creationID = input.CreationID
		owner.closing = &shutdown{
			barrier: session.WriteBarrier{
				SessionID: identifier,
			},
		}
		reply.Response(Closed{
			Barrier: owner.closing.barrier,
		})
	})
}
