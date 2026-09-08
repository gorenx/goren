package agentloop

import (
	"errors"
	"github.com/gorenx/goren/agent"
)

type activityKind uint8

const (
	executionActivity activityKind = iota
	maintenanceActivity
)

// activity owns one execution or maintenance interval without creating a Turn.
// wake latches input behind maintenance or cancellation; a live execution drains
// ordinary input itself.
type activity struct {
	kind         activityKind
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
	if subject.running == nil || subject.running.kind != executionActivity {
		return false, ErrState
	}
	return subject.finishActivity()
}

func (subject *ReactLoopAgent) finishActivity() (bool, error) {
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

// BeginMaintenance reserves an idle Agent without opening a durable Turn.
func (subject *ReactLoopAgent) BeginMaintenance() error {
	if err := subject.CheckExecution(); err != nil {
		return err
	}
	if subject.running != nil {
		return agent.ErrBusy
	}
	subject.running = &activity{
		kind: maintenanceActivity,
	}
	return nil
}

// FinishMaintenance releases a settled maintenance operation and reserves any
// requested execution whose input still exists. It performs no I/O or scheduling.
func (subject *ReactLoopAgent) FinishMaintenance() (bool, error) {
	if subject.running == nil || subject.running.kind != maintenanceActivity {
		return false, ErrState
	}
	return subject.finishActivity()
}
