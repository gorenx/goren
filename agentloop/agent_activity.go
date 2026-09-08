package agentloop

import "errors"

// activity records an accepted execution independently of its durable Turn.
// wake is only a request received after cancellation; ordinary running input is
// drained by the current execution and must not create a second execution.
type activity struct {
	cancellation error
	wake         bool
}

// Cancellation returns the accepted business intent for the current execution.
// The actor translates this value into cancellation of its I/O resources.
func (subject *ReactLoopAgent) Cancellation() error {
	if subject.running == nil {
		return nil
	}
	return subject.running.cancellation
}

// FinishActivity releases a finished execution after all business work is closed.
// True reserves another execution for input accepted after cancellation. Removing
// that input before completion suppresses the replay. No task is scheduled here.
func (subject *ReactLoopAgent) FinishActivity() (bool, error) {
	if subject.running == nil {
		return false, errors.New("agentloop: no execution is reserved")
	}
	if subject.current != nil || subject.step != nil ||
		subject.preparing != nil || subject.request != nil || subject.ending != nil {
		return false, errors.New("agentloop: execution still owns unfinished business work")
	}
	replay := subject.running.wake && subject.pending.HasPending()
	subject.running = nil
	if replay {
		subject.running = &activity{}
	}
	return replay, nil
}
