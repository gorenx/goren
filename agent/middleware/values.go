package middleware

import (
	"errors"
	"fmt"
	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/llm"
)

func cloneDecision(decision agent.PreStepDecision) (agent.PreStepDecision, error) {
	switch decision.Kind {
	case agent.PreStepReject:
		if len(decision.Messages) != 0 {
			return agent.PreStepDecision{}, errors.New("agent middleware: reject cannot carry entered messages")
		}
		return agent.PreStepDecision{
			Kind: agent.PreStepReject,
		}, nil
	case agent.PreStepEnter:
		copied, err := cloneMessages(decision.Messages)
		if err != nil {
			return agent.PreStepDecision{}, err
		}
		return agent.PreStepDecision{
			Kind:     agent.PreStepEnter,
			Messages: copied,
		}, nil
	default:
		return agent.PreStepDecision{}, errors.New("agent middleware: invalid pre-step decision")
	}
}

func cloneMessages(entries []agentmessage.UserMessage) ([]agentmessage.UserMessage, error) {
	if entries == nil {
		return nil, nil
	}
	result := make([]agentmessage.UserMessage, len(entries))
	for index, entry := range entries {
		copied, err := agentmessage.CloneUserMessage(entry)
		if err != nil {
			return nil, fmt.Errorf("agent middleware: clone input %d: %w", index, err)
		}
		result[index] = copied
	}
	return result, nil
}

func clonePreStep(input agent.PreStepInput) (agent.PreStepInput, error) {
	copied, err := cloneMessages(input.Messages)
	input.Messages = copied
	return input, err
}

func cloneRequest(input agent.RequestInput) (agent.RequestInput, error) { return input, nil }
func cloneConfig(input llm.CallConfig) (llm.CallConfig, error) {
	return llm.CloneCallConfig(input), nil
}
func cloneFailure(input agent.RequestFailure) (agent.RequestFailure, error) {
	if input.Failure.Status != nil {
		copied := *input.Failure.Status
		input.Failure.Status = &copied
	}
	if input.Failure.ProviderRetryAfterMS != nil {
		copied := *input.Failure.ProviderRetryAfterMS
		input.Failure.ProviderRetryAfterMS = &copied
	}
	if input.RetryPolicy != nil {
		input.RetryPolicy = input.RetryPolicy.CloneRetryPolicy()
	}
	return input, nil
}
func cloneAction(input agent.RequestErrorAction) (agent.RequestErrorAction, error) { return input, nil }
