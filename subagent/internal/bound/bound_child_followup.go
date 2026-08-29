package bound

import (
	"context"

	"github.com/gorenx/goren/agentmessage"
)

func (child *boundChild) handleFollowup(
	requestContext context.Context,
	messageValue agentmessage.UserMessage,
) (agentmessage.MessageID, error) {
	current, err := child.receivingEpoch(requestContext)
	if err != nil {
		return "", err
	}
	if err := current.followup(requestContext, messageValue); err != nil {
		return "", err
	}
	return messageValue.StableID(), nil
}
