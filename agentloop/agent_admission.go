package agentloop

import (
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
)

var _ agent.Agent = (*ReactLoopAgent)(nil)

// ID returns the restored business identity, never an activation identifier.
func (subject *ReactLoopAgent) ID() session.SessionID { return subject.identifier }

// InboxValue exposes only queries. The runtime must serialize reads and mutations.
func (subject *ReactLoopAgent) InboxValue() agent.InboxReader {
	if subject.pending == nil {
		return nil
	}
	return subject.pending
}

// StatusValue includes reserved execution before the first Turn is committed.
func (subject *ReactLoopAgent) StatusValue() agent.Status {
	if subject.state != executionIdle {
		return agent.StatusRunning
	}
	return agent.StatusIdle
}

// Followup proposes input for the next Turn and requests execution after commit.
func (subject *ReactLoopAgent) Followup(input agentmessage.UserMessage) (agent.Admission, error) {
	return subject.admit(agent.NextTurn, input, true)
}

// Steer proposes input for the next Step, or the next Turn after cancellation.
func (subject *ReactLoopAgent) Steer(input agentmessage.UserMessage) (agent.Admission, error) {
	return subject.admit(agent.NextStep, input, true)
}

// Inject proposes next-Step input without requesting an execution.
func (subject *ReactLoopAgent) Inject(input agentmessage.UserMessage) (agent.Admission, error) {
	return subject.admit(agent.NextStep, input, false)
}

func (subject *ReactLoopAgent) admit(target agent.InboxTarget, input agentmessage.UserMessage, wake bool) (agent.Admission, error) {
	if err := subject.CheckExecution(); err != nil {
		return agent.Admission{}, err
	}
	if wake && subject.cancelCause != nil {
		target = agent.NextTurn
	}
	edit, err := subject.pending.PrepareAppend(target, input)
	if err != nil {
		return agent.Admission{}, err
	}
	return agent.Admission{
		Edit: edit,
		Wake: wake,
	}, nil
}

// ConfirmAdmission applies committed input before reserving or requesting execution.
// The caller must serialize proposal and acknowledgement with other Inbox writes.
func (subject *ReactLoopAgent) ConfirmAdmission(proposal agent.Admission, write session.WriteResult) (agent.InboxChanges, error) {
	changes, err := subject.ConfirmEdit(proposal.Edit, write)
	if err != nil {
		return agent.InboxChanges{}, err
	}
	subject.acceptExecution(proposal.Wake)
	return changes, nil
}

// Cancel records the first intent and proposes optional Inbox removal.
// It does not stop I/O or apply the removal before the Session acknowledgement.
func (subject *ReactLoopAgent) Cancel(intent agent.CancelCause, options agent.CancelOptions) (agent.InboxEdit, error) {
	if !subject.lifetime.ready() || subject.pending == nil {
		return agent.InboxEdit{}, errors.New("agentloop: Agent is not initialized")
	}
	subject.cancelExecution(intent, options.KeepInbox)
	var edit agent.InboxEdit
	if !options.KeepInbox {
		if err := subject.checkInboxMutation(); err != nil {
			return agent.InboxEdit{}, err
		}
		var err error
		edit, err = subject.pending.PrepareClear()
		if err != nil {
			return agent.InboxEdit{}, err
		}
	}
	subject.preserveInbox = options.KeepInbox
	return edit, nil
}

// Promote moves a queued next-turn occurrence using one durable Inbox edit.
// Cancellation keeps the input in next-turn, matching ordinary Steer admission.
func (subject *ReactLoopAgent) Promote(identifier agentmessage.MessageID) (agent.Admission, error) {
	if err := subject.CheckExecution(); err != nil {
		return agent.Admission{}, err
	}
	found := false
	for _, input := range subject.pending.NextTurn() {
		if input.StableID() == identifier {
			found = true
			break
		}
	}
	if !found {
		for _, input := range subject.pending.NextStep() {
			if input.StableID() == identifier {
				return agent.Admission{}, agent.ErrSteerUnavailable
			}
		}
		return agent.Admission{}, agent.ErrInputMissing
	}
	if subject.state == executionIdle {
		return agent.Admission{}, agent.ErrSteerUnavailable
	}
	target := agent.NextStep
	if subject.cancelCause != nil {
		target = agent.NextTurn
	}
	edit, err := subject.pending.PrepareMove(identifier, target)
	if err != nil {
		return agent.Admission{}, err
	}
	return agent.Admission{
		Edit: edit,
		Wake: true,
	}, nil
}
