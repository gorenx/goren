package agentloop

import (
	"errors"

	"github.com/gorenx/goren/agent"
)

// executionState describes admission and exclusion, not the internal Turn phase.
type executionState uint8

const (
	// executionIdle admits a new execution or maintenance operation.
	executionIdle executionState = iota
	// executionRunning owns accepted execution, including before Turn start.
	executionRunning
	// executionMaintenance excludes Turn execution until maintenance completes.
	executionMaintenance
)

// Cancellation represents an accepted stop intent as a typed operation error.
// It returns nil when no cancellation was requested.
func (subject *ReactLoopAgent) Cancellation() error {
	if subject.cancelCause == nil {
		return nil
	}
	return agent.NewCancellation(subject.cancelCause)
}

// acceptExecution applies only a committed admission. An old preserved Inbox
// alone cannot request replay; a newly admitted wake can.
func (subject *ReactLoopAgent) acceptExecution(wake bool) {
	// A previously admitted commit may arrive after close admission was frozen.
	// Preserve its durable input without waking another execution.
	if subject.lifetime != available {
		return
	}
	subject.preserveInbox = false
	if !wake {
		return
	}
	if subject.state == executionIdle {
		subject.state = executionRunning
		return
	}
	if subject.state != executionMaintenance && subject.cancelCause == nil {
		return
	}
	if subject.cancelCause != nil && subject.cancelCause.CancelKind() == "disposed" {
		return
	}
	subject.runAfterCompletion = true
}

func (subject *ReactLoopAgent) cancelExecution(intent agent.CancelCause, keepInbox bool) {
	if subject.state == executionIdle {
		return
	}
	if subject.cancelCause == nil {
		requested := agent.NewCancellation(intent)
		subject.cancelCause = requested.Cause
	}
	if !keepInbox {
		subject.runAfterCompletion = false
	}
}

// FinishExecution releases execution only after its business work is closed.
// True reserves execution for remaining input that requested continuation.
func (subject *ReactLoopAgent) FinishExecution() (bool, error) {
	if subject.state != executionRunning {
		return false, ErrState
	}
	return subject.releaseWork()
}

func (subject *ReactLoopAgent) releaseWork() (bool, error) {
	if subject.current != nil ||
		subject.step != nil ||
		subject.preparing != nil ||
		subject.request != nil ||
		subject.ending != nil {
		return false, errors.New("agentloop: execution still owns unfinished business work")
	}
	replay := subject.runAfterCompletion && subject.pending.HasPending() &&
		!(subject.preserveInbox && subject.lifetime == closingAvailable)
	subject.state = executionIdle
	subject.cancelCause = nil
	subject.runAfterCompletion = false
	subject.failure = nil
	subject.retry = nil
	if replay {
		subject.state = executionRunning
	}
	return replay, nil
}

// BeginMaintenance reserves an idle Agent without opening a durable Turn.
func (subject *ReactLoopAgent) BeginMaintenance() error {
	err := subject.CheckExecution()
	if err != nil {
		return err
	}
	if subject.state != executionIdle {
		return agent.ErrBusy
	}
	subject.state = executionMaintenance
	return nil
}

// FinishMaintenance releases settled maintenance and reserves requested execution
// only when the input that requested it still exists. It performs no I/O.
func (subject *ReactLoopAgent) FinishMaintenance() (bool, error) {
	if subject.state != executionMaintenance {
		return false, ErrState
	}
	return subject.releaseWork()
}

// ContinueExecution reserves another Turn for pending input after a normal
// completion. Cancelled execution requires an explicit request to resume.
func (subject *ReactLoopAgent) ContinueExecution() (bool, error) {
	if subject.state == executionIdle {
		return false, nil
	}
	if subject.state != executionRunning {
		return false, ErrState
	}
	if subject.cancelCause == nil && subject.pending.HasPending() && !subject.preserveInbox {
		subject.runAfterCompletion = true
	}
	return subject.FinishExecution()
}
