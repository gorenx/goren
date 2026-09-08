package loopactor

import (
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Inbox commands propose mutations; Session commit acknowledgement precedes business confirmation.
func (owner *Grain) receiveInbox(gc *goakt.GrainContext) bool {
	switch input := gc.Message().(type) {
	case ReadActivity:
		owner.readActivity(gc, input)
	case Followup:
		owner.followup(gc, input)
	case Steer:
		owner.steer(gc, input)
	case Inject:
		owner.inject(gc, input)
	case Replace:
		owner.replace(gc, input)
	case Promote:
		owner.promote(gc, input)
	case Remove:
		owner.remove(gc, input)
	default:
		return false
	}
	return true
}
