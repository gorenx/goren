package agentloop

import (
	"errors"
	"reflect"

	"github.com/gorenx/goren/agent"
	stepstate "github.com/gorenx/goren/agentloop/internal/step"
	turnstate "github.com/gorenx/goren/agentloop/internal/turn"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/systemprompt"
)

// EndStep requests the prepared end event. Failure must remain available for
// Turn settlement after the event commits; a successful write does not erase it.
type EndStep struct {
	Draft session.EventDraft
	Err   error
}

func (subject *ReactLoopAgent) endStep(conclusion stepstate.StepResult, failure error) (EndStep, error) {
	draft, err := subject.PrepareStepEnd(conclusion)
	if err != nil {
		return EndStep{}, err
	}
	return EndStep{
		Draft: draft,
		Err:   failure,
	}, nil
}

// PrepareStep interprets admission without opening a durable Step. A nil draft
// means the Turn is settling and must proceed to its end boundary.
func (subject *ReactLoopAgent) PrepareStep(decision agent.PreStepDecision) (*session.EventDraft, error) {
	if failure := subject.Cancellation(); failure != nil {
		return nil, failure
	}
	if subject.step != nil {
		if subject.step.StateValue() != stepstate.StateProposed {
			return nil, ErrState
		}
		captured, err := subject.step.InputValue()
		if err != nil {
			return nil, err
		}
		proposed := stepstate.Input{
			Messages:     decision.Messages,
			Prompt:       captured.Prompt,
			ToolsVersion: captured.ToolsVersion,
		}
		if decision.Kind != agent.PreStepEnter || !reflect.DeepEqual(captured, proposed) {
			return nil, errors.New("agentloop: pending Step input cannot be replaced")
		}
		draft, err := subject.step.StartDraft()
		return &draft, err
	}
	position, err := subject.PreparePosition()
	if err != nil {
		return nil, err
	}
	if subject.preparing == nil || subject.preparing.state != decisionPending {
		return nil, errors.New("agentloop: pre-step preparation is missing")
	}
	switch decision.Kind {
	case agent.PreStepReject:
		err := subject.current.EnterSettling(turnstate.TurnResultBlocked)
		if err == nil {
			subject.preparing = nil
		}
		return nil, err
	case agent.PreStepEnter:
	default:
		return nil, errors.New("agentloop: unsupported pre-step decision")
	}
	conclusion := subject.current.ResultValue()
	if len(decision.Messages) == 0 {
		if conclusion != turnstate.TurnResultNone && conclusion != turnstate.TurnResultContinue {
			err := subject.current.EnterSettling(conclusion)
			if err == nil {
				subject.preparing = nil
			}
			return nil, err
		}
		if subject.current.LastStep() == 0 {
			err := subject.current.EnterSettling(turnstate.TurnResultCompleted)
			if err == nil {
				subject.preparing = nil
			}
			return nil, err
		}
	}
	proposed, err := stepstate.New(position.Turn, position.Step)
	if err != nil {
		return nil, err
	}
	if subject.preparing.toolsVersion != subject.toolsVersion {
		return nil, systemprompt.ErrToolsVersion
	}
	if err := proposed.Capture(stepstate.Input{
		Messages:     decision.Messages,
		Prompt:       subject.preparing.prompt,
		ToolsVersion: subject.preparing.toolsVersion,
	}); err != nil {
		return nil, err
	}
	draft, err := proposed.StartDraft()
	if err != nil {
		return nil, err
	}
	subject.step = proposed
	subject.preparing = nil
	return &draft, nil
}

// ConfirmStep advances the Turn and installs the Step only for the expected
// committed start. Callers retain admitted messages until their own commits.
func (subject *ReactLoopAgent) ConfirmStep(write session.WriteResult) error {
	if subject.step == nil || subject.step.StateValue() != stepstate.StateProposed {
		return ErrState
	}
	position, err := subject.PreparePosition()
	if err != nil {
		return err
	}
	draft, err := subject.step.StartDraft()
	if err != nil {
		return err
	}
	if err := subject.checkCommit([]session.EventDraft{draft}, write); err != nil {
		return err
	}
	opened := *subject.step
	enclosing := *subject.current
	if err := opened.ConfirmStart(write.Events[0]); err != nil {
		return err
	}
	if err := enclosing.EnterStepping(position.Step); err != nil {
		return err
	}
	subject.current = &enclosing
	subject.step = &opened
	return nil
}

// MessageDraft selects the next admitted message from the current Step snapshot.
func (subject *ReactLoopAgent) MessageDraft() (*session.EventDraft, error) {
	if subject.step == nil {
		return nil, errors.New("agentloop: no Step is open")
	}
	return subject.step.MessageDraft()
}

// ConfirmMessage accepts a committed input only from this Agent's Session.
func (subject *ReactLoopAgent) ConfirmMessage(write session.WriteResult) error {
	if subject.step == nil {
		return errors.New("agentloop: no Step is open")
	}
	if write.Barrier.SessionID != subject.identifier {
		return errors.New("agentloop: input commit Session identity mismatch")
	}
	return subject.step.ConfirmMessage(write)
}

// BeginRequest admits model execution after input messages have committed.
func (subject *ReactLoopAgent) BeginRequest() error {
	if failure := subject.Cancellation(); failure != nil {
		return failure
	}
	if subject.step == nil {
		return errors.New("agentloop: no Step is open")
	}
	return subject.step.EnterRequesting()
}

// BeginTools admits execution of tools in the committed assistant response.
func (subject *ReactLoopAgent) BeginTools() error {
	if err := subject.checkRequestCompletion(); err != nil {
		return err
	}
	if subject.step == nil {
		return errors.New("agentloop: no Step is open")
	}
	return subject.step.EnterTooling()
}

// PrepareStepEnd stages a conclusion without closing either execution boundary.
// Repeating the same preparation is safe after a failed commit.
func (subject *ReactLoopAgent) PrepareStepEnd(result stepstate.StepResult) (session.EventDraft, error) {
	if err := subject.checkRequestCompletion(); err != nil {
		return session.EventDraft{}, err
	}
	if subject.step == nil {
		return session.EventDraft{}, errors.New("agentloop: no Step is open")
	}
	candidate := *subject.step
	if candidate.StateValue() == stepstate.StateSettling {
		if candidate.ResultValue() != result {
			return session.EventDraft{}, errors.New("agentloop: Step conclusion is already selected")
		}
	} else if err := candidate.EnterSettling(result); err != nil {
		return session.EventDraft{}, err
	}
	draft, err := candidate.EndDraft()
	if err != nil {
		return session.EventDraft{}, err
	}
	subject.step = &candidate
	return draft, nil
}

// ConfirmStepEnd closes the Step and applies its conclusion to the enclosing Turn.
// Candidate copies keep both business objects unchanged on validation failure.
func (subject *ReactLoopAgent) ConfirmStepEnd(write session.WriteResult) error {
	if subject.step == nil || subject.current == nil {
		return errors.New("agentloop: no Step is open")
	}
	draft, err := subject.step.EndDraft()
	if err != nil {
		return err
	}
	if err := subject.checkCommit([]session.EventDraft{draft}, write); err != nil {
		return err
	}
	closed := *subject.step
	enclosing := *subject.current
	if err := closed.ConfirmEnd(write.Events[0]); err != nil {
		return err
	}
	if err := enclosing.EnterOpenAfterStep(turnstate.ResultFromStep(closed.ResultValue())); err != nil {
		return err
	}
	subject.current = &enclosing
	subject.step = nil
	return nil
}
