package loopactor

import (
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Turn messages bracket execution: start acknowledgement precedes Steps; settlement precedes release.
func (owner *Grain) receiveTurn(gc *goakt.GrainContext) bool {
	switch input := gc.Message().(type) {
	case startTurn:
		owner.startTurn(gc)
	case confirmTurnStart:
		owner.confirmTurnStart(gc, input)
	case continueTurn:
		owner.continueTurn(gc, input)
	case stoppingCompleted:
		owner.completeStopping(input)
	case finishTurn:
		owner.settleTurn(gc, input)
	case turnProgress:
		owner.advanceTurnEnd(gc, input)
	default:
		return false
	}
	return true
}
