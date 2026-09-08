package agentloop

import (
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentloop/internal/modelrequest"
	stepstate "github.com/gorenx/goren/agentloop/internal/step"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
)

// ModelAction describes the next Step effect after the model request finishes.
type ModelAction interface{ modelAction() }

// ExecuteTools contains model-order calls and their Step's definition version.
// Tool policy, middleware and execution remain the Tool Runtime's responsibility.
type ExecuteTools struct {
	Position     session.StepPosition
	ToolsVersion uint64
	Calls        []agentmessage.ToolCallBlock
}

func (ExecuteTools) modelAction() {}
func (EndStep) modelAction()      {}

// CompleteModel selects tools or Step settlement from the final request outcome.
// It reads only the currently owned request, never an externally supplied result.
func (subject *ReactLoopAgent) CompleteModel() (ModelAction, error) {
	if subject.request == nil {
		return nil, errors.New("agentloop: no model request is retained")
	}
	completed, err := subject.request.Outcome()
	if err != nil {
		return nil, err
	}
	next, err := subject.completeModel(completed.Result)
	if err == nil {
		subject.request = nil
	}
	return next, err
}

func (subject *ReactLoopAgent) completeModel(result modelrequest.Completed) (ModelAction, error) {
	if subject.step == nil || subject.step.StateValue() != stepstate.StateRequesting {
		return nil, errors.New("agentloop: model completion requires a current requesting Step")
	}
	subject.acceptModel(result)
	if !result.StreamClosed {
		return nil, errors.Join(result.Err, errors.New("agentloop: model resources must close before Step continuation"))
	}
	if result.Err != nil {
		conclusion := stepstate.StepResultError
		var cancelled agent.Cancellation
		if errors.As(result.Err, &cancelled) {
			conclusion = stepstate.StepResultAborted
		}
		return subject.endStep(conclusion, result.Err)
	}
	if result.Finish != nil {
		switch result.Finish.ReasonKind() {
		case "error", "aborted":
			return nil, errors.New("agentloop: failed model finish requires a final request error")
		}
	}
	response, err := agentmessage.CloneAssistantMessage(result.Message)
	if err != nil {
		return nil, err
	}
	if result.Finish != nil && result.Finish.ReasonKind() == "max-tokens" {
		return subject.endStep(stepstate.StepResultMaxTokens, nil)
	}
	var calls []agentmessage.ToolCallBlock
	for _, block := range response.ContentValue() {
		switch entry := block.(type) {
		case agentmessage.ToolCallBlock:
			calls = append(calls, entry)
		case *agentmessage.ToolCallBlock:
			if entry == nil {
				return nil, errors.New("agentloop: nil model tool call")
			}
			calls = append(calls, *entry)
		}
	}
	if len(calls) == 0 {
		return subject.endStep(stepstate.StepResultCompleted, nil)
	}
	captured, err := subject.step.InputValue()
	if err != nil {
		return nil, err
	}
	turnNumber, stepNumber := subject.step.Position()
	if err := subject.BeginTools(); err != nil {
		return nil, err
	}
	return ExecuteTools{
		Position: session.StepPosition{
			Turn: turnNumber,
			Step: stepNumber,
		},
		ToolsVersion: captured.ToolsVersion,
		Calls:        calls,
	}, nil
}
