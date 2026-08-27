package bound

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
)

// Send ensures the latest committed Bound config has one resident Agent epoch
// and then admits the message to that exact epoch under the same operation
// lock used by config replacement.
func (owner *Service) Send(
	ctx context.Context,
	parentAgent agent.Agent,
	childID session.SessionID,
	messageValue agentmessage.UserMessage,
) (agentmessage.MessageID, error) {
	if err := checkContext(ctx, "Bound Send"); err != nil {
		return "", err
	}
	if err := owner.authorizeParent(parentAgent); err != nil {
		return "", err
	}
	currentOperation := owner.childOperation(parentAgent.ID(), childID)
	currentOperation.mutex.Lock()
	defer currentOperation.mutex.Unlock()
	if _, err := owner.startLocked(
		ctx,
		parentAgent,
		childID,
		currentOperation,
	); err != nil {
		return "", err
	}
	current := currentOperation.loadCurrent()
	if current == nil || !agent.Same(
		current.terminator.parent,
		parentAgent,
	) {
		return "", errors.New(
			"subagent: Bound resident Agent is unavailable",
		)
	}
	if err := current.terminator.handle.Subject.Followup(
		messageValue,
	); err != nil {
		return "", err
	}
	return messageValue.StableID(), nil
}
