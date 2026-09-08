package agentloop

import (
	"errors"

	"github.com/gorenx/goren/agentloop/internal/modelrequest"
	stepstate "github.com/gorenx/goren/agentloop/internal/step"
	"github.com/gorenx/goren/session"
)

// StartRequest captures the current Step and starts its complete model request.
// The caller serializes execution and returns named results to AdvanceRequest.
// The Agent retains the driver until CompleteModel consumes it. Configuration and
// recovery belong to ModelRequest; this method does not resolve a provider.
// Close preparation still permits execution of already accepted input.
func (subject *ReactLoopAgent) StartRequest() (modelrequest.RequestOperation, error) {
	if !subject.lifetime.ready() || subject.pending == nil {
		return nil, errors.New("agentloop: Agent is not initialized")
	}
	if subject.step == nil || subject.step.StateValue() != stepstate.StateRequesting {
		return nil, errors.New("agentloop: model request requires a current requesting Step")
	}
	if subject.request != nil {
		return nil, errors.New("agentloop: current model request has not been consumed")
	}
	captured, err := subject.step.InputValue()
	if err != nil {
		return nil, err
	}
	turnNumber, stepNumber := subject.step.Position()
	request := modelrequest.New()
	next, err := request.Start(modelrequest.RequestInput{
		SessionID: subject.identifier,
		Position: session.StepPosition{
			Turn: turnNumber,
			Step: stepNumber,
		},
		Prompt: captured.Prompt,
		Config: subject.configuration.Config,
		First:  !subject.headerLogged,
	})
	if err != nil {
		return nil, err
	}
	subject.request = request
	return next, nil
}

// AdvanceRequest consumes one completion of the currently owned model request.
func (subject *ReactLoopAgent) AdvanceRequest(result modelrequest.RequestResult, cancellation error) (modelrequest.RequestOperation, error) {
	if subject.request == nil {
		return nil, errors.New("agentloop: no model request is active")
	}
	next, err := subject.request.Advance(result, cancellation)
	if completed, ok := next.(modelrequest.RequestFinished); ok {
		subject.AcceptModel(completed.Result)
	}
	return next, err
}

// RetryRequestClose preserves the final failure while retrying resource release.
func (subject *ReactLoopAgent) RetryRequestClose() (modelrequest.RequestOperation, error) {
	if subject.request == nil {
		return nil, errors.New("agentloop: no model request is retained")
	}
	return subject.request.RetryClose()
}

func (subject *ReactLoopAgent) checkRequestCompletion() error {
	if subject.request == nil {
		return nil
	}
	completed, err := subject.request.Outcome()
	if err != nil {
		return err
	}
	if !completed.Result.StreamClosed {
		return errors.New("agentloop: model resources must close before Step continuation")
	}
	return nil
}
