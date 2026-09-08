package agentloop

import (
	"errors"
	"github.com/gorenx/goren/agentloop/internal/modelrequest"
	stepstate "github.com/gorenx/goren/agentloop/internal/step"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

// ModelInput contains resolved provider metadata; Step owns Prompt and position.
type ModelInput struct {
	Config   llm.CallConfig
	Defaults llm.CallConfigAdapterDefaults
	Context  *llm.ModelContext
}

// BeginModel captures the prepared call metadata for an exclusively scheduled
// attempt. Attempt owns the complete business flow after construction.
// Close preparation still permits execution of already accepted input.
func (subject *ReactLoopAgent) BeginModel(input ModelInput) (*modelrequest.Attempt, error) {
	if !subject.lifetime.ready() || subject.pending == nil {
		return nil, errors.New("agentloop: Agent is not initialized")
	}
	if subject.step == nil || subject.step.StateValue() != stepstate.StateRequesting {
		return nil, errors.New("agentloop: model attempt requires a current Step")
	}
	captured, err := subject.step.InputValue()
	if err != nil {
		return nil, err
	}
	turnNumber, stepNumber := subject.step.Position()
	return modelrequest.NewAttempt(modelrequest.AttemptInput{
		SessionID: subject.identifier,
		Position: session.StepPosition{
			Turn: turnNumber,
			Step: stepNumber,
		},
		Prompt:   captured.Prompt,
		Config:   input.Config,
		Defaults: input.Defaults,
		Context:  input.Context,
		First:    !subject.headerLogged,
	})
}

// AcceptModel retains durable facts from a completed attempt, including failures.
func (subject *ReactLoopAgent) AcceptModel(result modelrequest.Completed) {
	if result.HeaderCommitted {
		subject.headerLogged = true
	}
}
