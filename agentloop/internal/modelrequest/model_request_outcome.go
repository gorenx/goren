package modelrequest

import (
	"errors"

	"github.com/gorenx/goren/agentmessage"
)

// Outcome returns detached terminal facts, including resource-release status.
// Recovery and cleanup in progress are not final outcomes.
func (current *ModelRequest) Outcome() (RequestFinished, error) {
	if current == nil || current.phase != requestComplete {
		return RequestFinished{}, errors.New("agentloop: model request has not finished")
	}
	result := current.outcome
	var err error
	if result.Message.StableID() != "" {
		result.Message, err = agentmessage.CloneAssistantMessage(result.Message)
		if err != nil {
			return RequestFinished{}, err
		}
	}
	if result.Finish != nil {
		result.Finish, err = result.Finish.CloneReason()
		if err != nil {
			return RequestFinished{}, err
		}
	}
	return RequestFinished{
		Result: result,
	}, nil
}
