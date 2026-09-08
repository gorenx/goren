package loopactor

import (
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Initialization advances authorization, Session restoration, model selection and Prompt binding.
func (owner *Grain) receiveInitialization(gc *goakt.GrainContext) bool {
	switch input := gc.Message().(type) {
	case Initialize:
		owner.initialize(gc, input)
	case initializationAuthorized:
		owner.startSession(gc, input)
	case initializationSession:
		owner.restoreInbox(gc, input)
	case initializationRestored:
		owner.initializeModel(gc, input)
	case initializationModel:
		owner.initializePrompt(gc, input)
	default:
		return false
	}
	return true
}
