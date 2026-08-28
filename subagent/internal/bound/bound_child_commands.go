package bound

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agentmessage"
)

func (child *boundChild) handleMessage(
	requestContext context.Context,
	messageValue agentmessage.UserMessage,
) (agentmessage.MessageID, error) {
	if err := child.handleAlignment(requestContext); err != nil {
		return "", err
	}
	definitionValue, found := child.definitions.find(child.key.name)
	if !found {
		return "", errors.New(
			"subagent: Bound Definition is unavailable",
		)
	}
	if !definitionValue.Enabled {
		return "", boundDisabled(child.key.childID)
	}
	current := child.current
	if current == nil {
		return "", errors.New(
			"subagent: Bound resident Agent is unavailable",
		)
	}
	if err := current.handle.Subject.Followup(messageValue); err != nil {
		return "", err
	}
	return messageValue.StableID(), nil
}
