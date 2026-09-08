package loopactor

import (
	agentactor "github.com/gorenx/goren/agent/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Initialization advances authorization, Session restoration, model selection and Prompt binding.
func (owner *Grain) receiveInitialization(gc *goakt.GrainContext) bool {
	switch input := gc.Message().(type) {
	case agentactor.Initialize:
		owner.initialize(gc, input)
	case initializationAuthorized:
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
