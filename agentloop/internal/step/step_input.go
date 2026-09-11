package step

import (
	"errors"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/systemprompt"
)

// Input contains this Step's admitted messages and frozen prompt data.
// Messages are the admission batch, not the full conversation history.
// Each model attempt derives its history from the committed Session.
type Input struct {
	Messages []agentmessage.UserMessage
	Prompt   systemprompt.PromptAssembly
}

// Capture binds detached input once, before step/start is committed.
func (current *Step) Capture(provided Input) error {
	if current.currentState != StateProposed || current.input != nil {
		return errors.New("agentloop step: input can only be captured once before opening")
	}
	detached, err := copyInput(provided)
	if err != nil {
		return err
	}
	current.input = &detached
	return nil
}

// InputValue returns a detached snapshot, never the mutable execution owner.
func (current *Step) InputValue() (Input, error) {
	if current.input == nil {
		return Input{}, errors.New("agentloop step: input has not been captured")
	}
	return copyInput(*current.input)
}

func copyInput(provided Input) (Input, error) {
	prompt, err := provided.Prompt.Clone()
	if err != nil {
		return Input{}, err
	}
	detached := Input{
		Prompt: prompt,
	}
	if provided.Messages != nil {
		detached.Messages = make([]agentmessage.UserMessage, len(provided.Messages))
		for index, entry := range provided.Messages {
			copied, err := agentmessage.CloneUserMessage(entry)
			if err != nil {
				return Input{}, err
			}
			detached.Messages[index] = copied
		}
	}
	return detached, nil
}
