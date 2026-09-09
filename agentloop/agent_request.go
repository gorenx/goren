package agentloop

import (
	"errors"
	"github.com/gorenx/goren/agent"
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
	if failure := subject.Cancellation(); failure != nil {
		return nil, failure
	}
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
		First:  !subject.headerLogged,
	})
	if err != nil {
		return nil, err
	}
	subject.request = request
	return next, nil
}

// AdvanceRequest consumes one completion together with the Agent-owned cancellation.
// The caller cannot omit or replace an already accepted cancellation intent.
func (subject *ReactLoopAgent) AdvanceRequest(result modelrequest.RequestResult) (modelrequest.RequestOperation, error) {
	if subject.request == nil {
		return nil, errors.New("agentloop: no model request is active")
	}
	next, err := subject.request.Advance(result, subject.Cancellation())
	if completed, ok := next.(modelrequest.RequestFinished); ok {
		subject.acceptModel(completed.Result)
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

// acceptModel retains durable facts from a completed attempt, including failures.
func (subject *ReactLoopAgent) acceptModel(result modelrequest.Completed) {
	if result.HeaderCommitted {
		subject.headerLogged = true
	}
}

func (work *ReactLoopAgent) startRequest(services Services) {
	next, err := work.StartRequest()
	if err != nil {
		work.failStep(services, err)
		return
	}
	work.requestModel(services, next)
}

func (work *ReactLoopAgent) requestModel(services Services, operation modelrequest.RequestOperation) {
	switch selected := operation.(type) {
	case modelrequest.RunAttempt:
		work.runModelOperation(services, selected.Operation)
	case modelrequest.ResolveRecovery:
		err := services.Middleware.RequestError(selected.Input, work)
		if err != nil {
			work.RequestErrorResolved(services, agent.RequestErrorResolved{Err: err})
		}
	case modelrequest.RequestFinished:
		if !selected.Result.StreamClosed {
			work.retry = modelCloseRetry{}
			services.Observer.ExecutionFailed(errors.Join(selected.Result.Err, errors.New("agentloop: model stream close is unconfirmed")))
			return
		}
		next, err := work.CompleteModel()
		if err != nil {
			work.failStep(services, errors.Join(selected.Result.Err, err))
			return
		}
		switch action := next.(type) {
		case EndStep:
			work.commitStepEnd(services, action)
		case ExecuteTools:
			work.executeTools(services, action)
		default:
			services.Observer.ExecutionFailed(errors.New("agentloop: invalid model conclusion"))
		}
	default:
		services.Observer.ExecutionFailed(errors.New("agentloop: invalid model request operation"))
	}
}

// modelCloseRetry retains only the business decision to retry resource release.
// The current ModelRequest keeps the attempt identity and original outcome.
type modelCloseRetry struct{}

func (modelCloseRetry) resume(work *ReactLoopAgent, services Services) {
	next, err := work.RetryRequestClose()
	if err != nil {
		work.retry = modelCloseRetry{}
		services.Observer.ExecutionFailed(err)
		return
	}
	work.requestModel(services, next)
}

func (work *ReactLoopAgent) advanceRequest(services Services, result modelrequest.RequestResult) {
	next, err := work.AdvanceRequest(result)
	if err != nil {
		work.failStep(services, err)
		return
	}
	work.requestModel(services, next)
}

func (work *ReactLoopAgent) RequestErrorResolved(services Services, result agent.RequestErrorResolved) {
	if result.Err != nil {
		work.advanceRequest(services, modelrequest.RequestFailed{Err: result.Err})
		return
	}
	work.advanceRequest(services, modelrequest.RecoveryResolved{Action: result.Action})
}

// runModelOperation sends the effect selected by the current model request.
// Results return directly to the owning Agent; no forwarding receiver is needed.
func (work *ReactLoopAgent) runModelOperation(services Services, operation modelrequest.Operation) {
	var err error
	switch selected := operation.(type) {
	case modelrequest.ReadLog:
		err = services.Conversation.Read(work)
	case modelrequest.CommitEvents:
		err = services.Conversation.Commit(session.Batch(selected.Draft), work)
	case modelrequest.Generate:
		err = services.Models.Generate(selected.Input, work)
	case modelrequest.ReadChunk:
		err = services.Models.Read(work)
	case modelrequest.CloseStream:
		err = services.Models.Close(work)
	default:
		err = errors.New("agentloop: invalid generation operation")
	}
	if err != nil {
		work.acceptModelOperation(services, modelrequest.Failed{Err: err})
	}
}

func (work *ReactLoopAgent) acceptModelOperation(services Services, result modelrequest.Result) {
	work.advanceRequest(services, modelrequest.AttemptAdvanced{Result: result})
}

func (work *ReactLoopAgent) SessionRead(services Services, snapshot session.Snapshot, failure error) {
	if failure != nil {
		work.acceptModelOperation(services, modelrequest.Failed{Err: failure})
		return
	}
	work.acceptModelOperation(services, modelrequest.LogRead{Snapshot: snapshot})
}

func (work *ReactLoopAgent) SessionCommitted(services Services, result session.WriteResult, failure error) {
	if failure != nil {
		work.acceptModelOperation(services, modelrequest.Failed{Err: failure})
		return
	}
	work.acceptModelOperation(services, modelrequest.EventsCommitted{Write: result})
}

func (work *ReactLoopAgent) GenerationStarted(services Services, result modelrequest.GenerationStarted) {
	work.acceptModelOperation(services, result)
}

func (work *ReactLoopAgent) ChunkRead(services Services, result modelrequest.ChunkRead, failure error) {
	if failure != nil {
		work.acceptModelOperation(services, modelrequest.Failed{Err: failure})
		return
	}
	work.acceptModelOperation(services, result)
}

func (work *ReactLoopAgent) StreamClosed(services Services, failure error) {
	if failure != nil {
		work.acceptModelOperation(services, modelrequest.Failed{Err: failure})
		return
	}
	work.acceptModelOperation(services, modelrequest.StreamClosed{})
}
