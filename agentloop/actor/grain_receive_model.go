package loopactor

import (
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Model requests require a prepared Step; stream and persistence completions advance the same request.
func (owner *Grain) receiveModel(gc *goakt.GrainContext) bool {
	switch input := gc.Message().(type) {
	case requestModel:
		owner.startModel(gc, input)
	case requestProgress:
		owner.advanceRequest(gc, input)

	case streamProgress:
		owner.advanceStream(gc, input)
	default:
		return false
	}
	return true
}
