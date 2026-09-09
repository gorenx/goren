package execution

import (
	"context"
	"fmt"
	"strings"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/approval"
	"github.com/gorenx/goren/tools"
)

type policyEngine struct {
	guards []tools.ToolGuard
}

type preDispatchEvaluation struct {
	denialReason string
	approval     *tools.ApprovalRequest
}

func (engine *policyEngine) evaluate(
	ctx context.Context,
	toolCall callExecution,
) (preDispatchEvaluation, error) {
	gateOutcome, err := toolCall.state.pipeline.EvaluateCall(
		ctx,
		tools.NewPreExecuteRequest(cloneExecution(toolCall)),
	)
	if err != nil {
		return preDispatchEvaluation{}, err
	}
	ask, needsApproval := gateOutcome.Decision.(tools.AskDecision)
	if needsApproval {
		return preDispatchEvaluation{
			approval: &tools.ApprovalRequest{
				SessionID: toolCall.SessionID,
				ToolName:  toolCall.Name,
				CallID:    toolCall.CallID,
				Reason:    ask.Reason,
			},
		}, nil
	}
	denialReason := decisionDenial(toolCall.Name, gateOutcome.Decision)
	if denialReason == "" {
		denialReason, err = engine.guardDenial(toolCall)
		if err != nil {
			return preDispatchEvaluation{}, err
		}
	}
	return preDispatchEvaluation{
		denialReason: denialReason,
	}, nil
}

func (engine *policyEngine) guardDenial(
	toolCall callExecution,
) (string, error) {
	for _, policy := range engine.guards {
		reason, denied, err := invokeGuard(policy, toolCall)
		if err != nil {
			return "", err
		}
		if denied {
			if strings.TrimSpace(reason) == "" {
				return "tool execution denied by guard", nil
			}
			return reason, nil
		}
	}
	return "", nil
}

func approvalDecision(name string, decisionOutcome approval.Outcome) (tools.PreToolDecision, bool, error) {
	switch decisionOutcome {
	case approval.OutcomeAllowedOnce:
		return tools.AllowDecision{}, false, nil
	case approval.OutcomeRejected:
		return tools.DenyDecision{
			Reason: fmt.Sprintf(
				"the user rejected tool %q",
				name,
			),
		}, false, nil
	case approval.OutcomeCancelled:
		return tools.DenyDecision{
			Reason: fmt.Sprintf(
				"approval for tool %q was cancelled",
				name,
			),
		}, true, nil
	case approval.OutcomeUnavailable:
		return tools.DenyDecision{
			Reason: fmt.Sprintf(
				"tool %q requires approval, but no approval channel is available",
				name,
			),
		}, false, nil
	default:
		return nil, false, fmt.Errorf(
			"tools: unsupported ApprovalOutcome %q",
			decisionOutcome,
		)
	}
}

func decisionDenial(name string, decision tools.PreToolDecision) string {
	switch selected := decision.(type) {
	case tools.AllowDecision:
		return ""
	case tools.DenyDecision:
		return selected.Reason
	case tools.AskDecision:
		return fmt.Sprintf(
			"tool %q approval decision was not resolved",
			name,
		)
	case nil:
		return "pre-execute policy returned no decision"
	default:
		return fmt.Sprintf(
			"unsupported pre-execute decision %T",
			decision,
		)
	}
}

func deniedResult(reason string) tools.ToolExecutionResult {
	return &tools.ToolExecutionFailure{
		Error: tools.ToolFailure{
			Message: reason,
		},
		Content: []agentmessage.ContentBlock{
			agentmessage.NewTextBlock("Error: " + reason),
		},
	}
}

func invokeGuard(
	policy tools.ToolGuard,
	toolCall callExecution,
) (reason string, denied bool, invokeErr error) {
	defer func() {
		panicValue := recover()
		if panicValue != nil {
			invokeErr = fmt.Errorf(
				"tools: guard panicked: %v",
				panicValue,
			)
		}
	}()
	reason, denied = policy.DenyReason(cloneExecution(toolCall))
	return reason, denied, nil
}
