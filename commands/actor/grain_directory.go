package commandactor

import (
	"errors"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

func (owner *Grain) listCommands(gc *goakt.GrainContext, input ListCommands) {
	directoryRoute := owner.binding.Registry
	if directoryRoute == nil ||
		input.CreationID == "" ||
		input.CreationID != owner.binding.CreationID {
		gc.Response(Listed{
			Err: errors.New("commands: current initialized creation is required"),
		})
		return
	}
	reply := gc.DeferResponse()
	gc.RequestGrain(directoryRoute, List{
		SessionID: owner.sessionID,
	}, goakt.WithRequestTimeout(owner.binding.Timeout), goakt.WithReentrancyMode(reentrancy.AllowAll)).Then(func(payload any, failure error) {
		result, ok := payload.(Descriptors)
		if failure == nil && !ok {
			failure = errors.New("commands: invalid directory reply")
		}
		if owner.binding.Registry != directoryRoute {
			failure = errors.Join(failure, errors.New("commands: execution owner closed during directory query"))
		}
		reply.Response(Listed{
			Values: result.Values,
			Err:    failure,
		})
	})
}
