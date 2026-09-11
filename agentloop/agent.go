// Package agentloop owns one Agent's business state independently of actor scheduling.
package agentloop

import (
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentloop/internal/modelrequest"
	stepstate "github.com/gorenx/goren/agentloop/internal/step"
	turnstate "github.com/gorenx/goren/agentloop/internal/turn"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
)

// ErrPending means accepted work must finish or be explicitly cancelled.
var ErrPending = errors.New("agentloop: accepted input remains")

// ReactLoopAgent owns admission and restored execution data. Its caller serializes access
// and reports committed events; this object performs no I/O or scheduling.
type ReactLoopAgent struct {
	// inboxNext excludes already-applied log entries when reconciling an uncertain claim.
	inboxNext  int64
	request    *modelrequest.ModelRequest
	preparing  *preparation
	identifier session.SessionID
	ending     *settlement
	pending    *agent.Inbox
	current    *turnstate.Turn
	step       *stepstate.Step
	lastTurn   int64

	state executionState
	// cancelCause identifies who requested stopping; nil means no cancellation.
	// It is an intent, not an execution failure.
	cancelCause agent.CancelCause
	// runAfterCompletion requests execution after current work finishes.
	// Cancelling with old input A preserved leaves it false; admitting new
	// Followup B after cancellation sets it true. Maintenance uses the same rule.
	runAfterCompletion bool
	// failure retains the execution outcome, including reported cancellation,
	// until the current Turn is durable. It does not request cancellation.
	failure error
	// retry retains an immutable failed boundary, never a transport receiver.
	retry executionRetry

	lifetime     lifecycle
	headerLogged bool
	// preserveInbox records explicit cancellation intent until another input is admitted.
	// Pending durable input may survive close; active work must still settle.
	preserveInbox bool
}

// Restore installs a complete log projection.
// Parsing failures leave the previous state untouched.
func (subject *ReactLoopAgent) Restore(metadata session.Header, snapshot session.Snapshot) error {
	if subject.lifetime != unrestored {
		return ErrState
	}
	if snapshot.Barrier.SessionID != metadata.ID || snapshot.Barrier.NextSeq != int64(len(snapshot.Events)) {
		return errors.New("agentloop: Session read identity mismatch")
	}
	pending, err := agent.NewInbox(metadata, snapshot.Events)
	if err != nil {
		return err
	}
	number, err := turnstate.LastNumber(snapshot.Events)
	if err != nil {
		return err
	}
	for index, entry := range snapshot.Events {
		if entry.Seq != int64(index) {
			return errors.New("agentloop: recovery snapshot sequence is discontinuous")
		}
	}
	subject.inboxNext = snapshot.Barrier.NextSeq
	subject.identifier = metadata.ID
	subject.pending = pending
	subject.lastTurn = number
	subject.lifetime = restored
	return nil
}

// Initialize admits work after the currently required components have initialized.
func (subject *ReactLoopAgent) Initialize() error {
	if subject.lifetime != restored {
		return ErrState
	}
	subject.lifetime = available
	return nil
}

// CheckExecution rejects new work before initialization or after close preparation.
func (subject *ReactLoopAgent) CheckExecution() error {
	if err := subject.lifetime.checkAdmission(); err != nil {
		return err
	}
	return subject.checkInboxMutation()
}

// Inbox returns pending inputs, including a restored failed initialization.
func (subject *ReactLoopAgent) Inbox() (agent.Pending, error) {
	if subject.pending == nil {
		return agent.Pending{}, errors.New("agentloop: Agent is not initialized")
	}
	return agent.Pending{
		NextTurn: subject.pending.NextTurn(),
		NextStep: subject.pending.NextStep(),
	}, nil
}

// Replace prepares a replacement while input admission remains open.
func (subject *ReactLoopAgent) Replace(identifier agentmessage.MessageID, input agentmessage.UserMessage) (agent.InboxEdit, error) {
	if err := subject.CheckExecution(); err != nil {
		return agent.InboxEdit{}, err
	}
	return subject.pending.PrepareReplace(identifier, input)
}

// Remove allows cancellation of pending input after close preparation.
func (subject *ReactLoopAgent) Remove(identifier agentmessage.MessageID) (agent.InboxEdit, error) {
	if err := subject.checkInboxMutation(); err != nil {
		return agent.InboxEdit{}, err
	}
	if !subject.lifetime.ready() || subject.pending == nil {
		return agent.InboxEdit{}, errors.New("agentloop: Agent is not initialized")
	}
	return subject.pending.PrepareRemove(identifier)
}

// Apply folds only successfully committed Inbox events.
func (subject *ReactLoopAgent) Apply(entries []session.Event) (agent.InboxChanges, error) {
	if subject.pending == nil {
		return agent.InboxChanges{}, errors.New("agentloop: Agent is not initialized")
	}
	if len(entries) > 0 && entries[0].Seq < subject.inboxNext {
		return agent.InboxChanges{}, ErrState
	}
	changes, err := subject.pending.Apply(entries)
	if err != nil {
		return agent.InboxChanges{}, err
	}
	if len(entries) > 0 {
		subject.inboxNext = entries[len(entries)-1].Seq + 1
	}
	return changes, nil
}

// PrepareClose freezes admission and preserves input whose execution has not
// started. A proposed Turn still owns an uncertain start commit and must settle.
func (subject *ReactLoopAgent) PrepareClose(executing bool) error {
	return subject.prepareClose(executing, false)
}

// Drain freezes input admission without cancelling active work. Unstarted input
// remains durable for restoration; completion still requires the current Flush.
func (subject *ReactLoopAgent) Drain(executing bool) error {
	if !subject.lifetime.ready() {
		return ErrState
	}
	return subject.prepareClose(executing, true)
}

func (subject *ReactLoopAgent) prepareClose(executing bool, finishCurrent bool) error {
	subject.lifetime.beginClose()
	subject.preserveInbox = true
	subject.runAfterCompletion = false
	if !finishCurrent && subject.state != executionIdle {
		subject.cancelExecution(agent.DisposedCancel{}, true)
	}
	if !executing &&
		subject.state == executionRunning &&
		subject.current == nil &&
		subject.step == nil &&
		subject.preparing == nil &&
		subject.request == nil &&
		subject.ending == nil {
		subject.preserveInbox = true
		subject.runAfterCompletion = false
		_, err := subject.FinishExecution()
		if err != nil {
			return err
		}
	}
	return subject.CheckClose(executing)
}

// CheckClose preserves historical input when cleaning up a failed initialization.
func (subject *ReactLoopAgent) CheckClose(executing bool) error {
	if executing || subject.state != executionIdle || subject.current != nil {
		return ErrPending
	}
	if err := subject.lifetime.checkRelease(); err != nil {
		return err
	}
	if subject.lifetime.ready() && !subject.preserveInbox && subject.pending != nil && subject.pending.HasPending() {
		return ErrPending
	}
	return nil
}

// Release discards local business state after resource cleanup succeeds.
func (subject *ReactLoopAgent) Release() error {
	if err := subject.CheckClose(false); err != nil {
		return err
	}
	*subject = ReactLoopAgent{
		lifetime: released,
	}
	return nil
}

func (subject *ReactLoopAgent) checkInboxMutation() error {
	if subject.preparing != nil && subject.preparing.state == inputUnconfirmed {
		return ErrState
	}
	return nil
}
