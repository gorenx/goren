package loopactor

import (
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Step preparation waits for admission before deriving model input. Each completion belongs to its original preparation.
func (owner *Grain) receiveStep(gc *goakt.GrainContext) bool {
	switch input := gc.Message().(type) {
	case prepareStep:
		owner.startStep(gc, input)
	case inputRecovered:
		owner.reconcileInput(gc, input)
	case inputClaimed:
		owner.claimConfirmed(gc, input)
	case contextRead:
		owner.preparePrompt(gc, input)
	case promptAssembled:
		owner.openStep(gc, input)
	case admissionResolved:
		owner.acceptStepAdmission(gc, input)
	case stepRecovered:
		owner.reconcileStep(gc, input)
	case stepStarted:
		owner.stepConfirmed(gc, input)
	case messageCommitted:
		owner.messageConfirmed(gc, input)
	case preparationFailed:
		owner.finishPreparation(input.operation, false, input.err)
	default:
		return false
	}
	return true
}
