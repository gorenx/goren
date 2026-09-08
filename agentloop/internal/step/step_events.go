package step

import (
	"errors"

	"github.com/gorenx/goren/session"
)

// StartDraft describes the proposed durable boundary without opening the Step.
// The caller must commit it and pass the event to ConfirmStart.
func (current *Step) StartDraft() (session.EventDraft, error) {
	if current == nil {
		return session.EventDraft{}, errors.New("agentloop step: Step is nil")
	}
	if current.currentState != StateProposed {
		return session.EventDraft{}, current.transitionError("prepare start")
	}
	return session.NewEventDraft(session.StepStarted, session.StepPosition{
		Turn: current.turnNumber,
		Step: current.stepNumber,
	})
}

// MessageDraft returns the next captured message, or nil when all have committed.
// Retrying before confirmation returns the same immutable message proposal.
func (current *Step) MessageDraft() (*session.EventDraft, error) {
	if current == nil {
		return nil, errors.New("agentloop step: Step is nil")
	}
	if current.currentState != StateOpen || current.input == nil {
		return nil, current.transitionError("prepare input")
	}
	if current.inputOffset == len(current.input.Messages) {
		return nil, nil
	}
	draft, err := session.NewSurfaceEventDraft(session.UserMessageAdded,
		current.input.Messages[current.inputOffset], session.SurfaceIntent{
			Operation: session.SurfaceAppend(),
		})
	if err != nil {
		return nil, err
	}
	return &draft, nil
}

// ConfirmMessage advances only after the expected message committed in order.
// The enclosing Agent validates the Session identity before calling this method.
func (current *Step) ConfirmMessage(write session.WriteResult) error {
	draft, err := current.MessageDraft()
	if err != nil {
		return err
	}
	if draft == nil {
		return errors.New("agentloop step: all admitted messages already committed")
	}
	if len(write.Events) != 1 || write.FirstSeq <= current.lastInputSeq ||
		write.Events[0].Seq != write.FirstSeq || write.NextSeq != write.FirstSeq+1 ||
		write.Barrier.NextSeq != write.NextSeq {
		return errors.New("agentloop step: invalid input commit acknowledgement")
	}
	if err := draft.CheckCommit(write.Events[0]); err != nil {
		return err
	}
	current.lastInputSeq = write.FirstSeq
	current.inputOffset++
	return nil
}

// EndDraft describes the settled Step boundary. ConfirmEnd consumes the
// matching committed event before the Step becomes closed.
func (current *Step) EndDraft() (session.EventDraft, error) {
	if current == nil {
		return session.EventDraft{}, errors.New("agentloop step: Step is nil")
	}
	if current.currentState != StateSettling {
		return session.EventDraft{}, current.transitionError("prepare end")
	}
	return session.NewEventDraft(session.StepEnded, session.StepPosition{
		Turn: current.turnNumber,
		Step: current.stepNumber,
	})
}
