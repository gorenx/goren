package sessionadapter

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	loopactor "github.com/gorenx/goren/agentloop/actor"
	"github.com/gorenx/goren/agentmessage"
)

// change erases transport types only at the framework boundary.
func (subject *Agent) change(ctx context.Context, input any) (agent.InboxChanges, error) {
	payload, err := subject.system.AskGrain(ctx, subject.endpoint, input, subject.timeout)
	if err != nil {
		return agent.InboxChanges{}, err
	}
	result, ok := payload.(loopactor.Changed)
	if !ok {
		return agent.InboxChanges{}, errors.New("session API: invalid input commit reply")
	}
	return result.Changes, result.Err
}

// Followup acknowledges an Inbox commit, not completion of the resulting turn.
func (subject *Agent) Followup(ctx context.Context, input agentmessage.UserMessage) error {
	_, err := subject.change(ctx, loopactor.Followup{
		CreationID: subject.creationID,
		Message:    input,
	})
	return err
}

// Steer submits input for the next boundary chosen by AgentLoop.
func (subject *Agent) Steer(ctx context.Context, input agentmessage.UserMessage) error {
	_, err := subject.change(ctx, loopactor.Steer{
		CreationID: subject.creationID,
		Message:    input,
	})
	return err
}

// Replace rejects a disappeared occurrence instead of reporting a successful edit.
func (subject *Agent) Replace(ctx context.Context, id agentmessage.MessageID, input agentmessage.UserMessage) error {
	changed, err := subject.change(ctx, loopactor.Replace{
		CreationID: subject.creationID,
		MessageID:  id,
		Message:    input,
	})
	if err == nil && len(changed.Inserted) == 0 {
		return agent.ErrInputMissing
	}
	return err
}

// Remove rejects a message claimed between a queue read and the removal request.
func (subject *Agent) Remove(ctx context.Context, id agentmessage.MessageID) error {
	changed, err := subject.change(ctx, loopactor.Remove{
		CreationID: subject.creationID,
		MessageID:  id,
	})
	if err == nil && len(changed.Discarded) == 0 {
		return agent.ErrInputMissing
	}
	return err
}

// Promote requests one atomic remove-and-insert commit.
func (subject *Agent) Promote(ctx context.Context, id agentmessage.MessageID) error {
	_, err := subject.change(ctx, loopactor.Promote{
		CreationID: subject.creationID,
		MessageID:  id,
	})
	return err
}

// Cancel acknowledges cancellation intent and any requested Inbox commit.
func (subject *Agent) Cancel(ctx context.Context, cause agent.CancelCause, options agent.CancelOptions) error {
	_, err := subject.change(ctx, loopactor.Cancel{
		CreationID: subject.creationID,
		Cause:      cause,
		Options:    options,
	})
	return err
}
