package agentloop

import (
	"errors"

	"github.com/gorenx/goren/agent"
	turnstate "github.com/gorenx/goren/agentloop/internal/turn"
	"github.com/gorenx/goren/session"
)

// PrepareTurn proposes the next durable start without claiming input or advancing
// the number. Close preparation still permits draining previously accepted input.
func (subject *ReactLoopAgent) PrepareTurn() (session.EventDraft, error) {
	if !subject.lifetime.ready() || subject.pending == nil {
		return session.EventDraft{}, errors.New("agentloop: Agent is not initialized")
	}
	if subject.current != nil && subject.current.StateValue() != turnstate.StateProposed {
		return session.EventDraft{}, ErrState
	}
	if subject.running == nil {
		return session.EventDraft{}, errors.New("agentloop: no execution is reserved")
	}
	if subject.running.cancellation != nil {
		return session.EventDraft{}, subject.running.cancellation
	}
	if subject.current != nil {
		return subject.current.StartDraft()
	}
	proposed, err := turnstate.New(subject.lastTurn + 1)
	if err != nil {
		return session.EventDraft{}, err
	}
	draft, err := proposed.StartDraft()
	if err != nil {
		return session.EventDraft{}, err
	}
	subject.current = proposed
	return draft, nil
}

// ConfirmTurn installs only the expected committed start. Failed confirmation
// leaves the current Turn and durable number unchanged.
func (subject *ReactLoopAgent) ConfirmTurn(write session.WriteResult) error {
	if !subject.lifetime.ready() || subject.current == nil || subject.current.StateValue() != turnstate.StateProposed {
		return ErrState
	}
	draft, err := subject.current.StartDraft()
	if err != nil {
		return err
	}
	if err := subject.checkCommit([]session.EventDraft{draft}, write); err != nil {
		return err
	}
	// Cancellation after dispatch cannot erase the committed start. The opened
	// Turn must still record its cancellation outcome and complete durability.
	if err := subject.current.EnterOpen(); err != nil {
		return err
	}
	subject.lastTurn = subject.current.Number()
	return nil
}

// PreparePosition describes the next Step without opening it or advancing progress.
func (subject *ReactLoopAgent) PreparePosition() (session.StepPosition, error) {
	if subject.current == nil {
		return session.StepPosition{}, errors.New("agentloop: no Turn is open")
	}
	number, err := subject.current.ProposedStep()
	if err != nil {
		return session.StepPosition{}, err
	}
	return session.StepPosition{
		Turn: subject.current.Number(),
		Step: number,
	}, nil
}

// PrepareInput selects the first-Step or continuation claim from Turn progress.
// ConfirmInput captures the claimed messages only after the Session confirms this edit.
func (subject *ReactLoopAgent) PrepareInput() (agent.InboxEdit, error) {
	if failure := subject.Cancellation(); failure != nil {
		return agent.InboxEdit{}, failure
	}
	if subject.preparing != nil || subject.step != nil {
		return agent.InboxEdit{}, ErrState
	}
	if _, err := subject.PreparePosition(); err != nil {
		return agent.InboxEdit{}, err
	}
	target := agent.NextStep
	if subject.current.LastStep() == 0 {
		target = agent.NextTurn
	}
	edit, err := subject.pending.PrepareClaim(target)
	if err != nil {
		return agent.InboxEdit{}, err
	}
	subject.preparing = &preparation{
		after: subject.inboxNext,
		edit:  edit,
	}
	if len(edit.Drafts) == 0 {
		subject.preparing.state = contextPending
	}
	return edit, nil
}

// BeginStopping enters the turn-stopping boundary only when a completed Step
// has no queued continuation input. False means execution must continue first.
func (subject *ReactLoopAgent) BeginStopping() (bool, error) {
	if subject.current == nil || subject.step != nil || subject.current.StateValue() != turnstate.StateOpen {
		return false, errors.New("agentloop: stopping requires an open Turn between Steps")
	}
	conclusion := subject.current.ResultValue()
	if conclusion == turnstate.TurnResultNone || conclusion == turnstate.TurnResultContinue || len(subject.pending.NextStep()) != 0 {
		return false, nil
	}
	if err := subject.current.EnterStopping(); err != nil {
		return false, err
	}
	return true, nil
}

// FinishStopping observes input committed during the stopping boundary. True
// resumes Steps; false fixes the existing conclusion for Turn settlement.
func (subject *ReactLoopAgent) FinishStopping() (bool, error) {
	if subject.current == nil || subject.current.StateValue() != turnstate.StateStopping {
		return false, errors.New("agentloop: turn-stopping boundary has not opened")
	}
	if len(subject.pending.NextStep()) != 0 {
		if err := subject.current.EnterOpenAfterStopping(); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, subject.current.EnterSettling(subject.current.ResultValue())
}
