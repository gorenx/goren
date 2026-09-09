package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/approval"
)

type policyEngine struct {
	guards []ToolGuard
}

type preDispatchEvaluation struct {
	denialReason string
	approval     *ApprovalRequest
}

func (engine *policyEngine) evaluate(
	ctx context.Context,
	toolCall ToolExecution,
) (preDispatchEvaluation, error) {
	gateOutcome, err := toolCall.state.pipeline.ResolvePreExecute(
		ctx,
		PreExecuteRequest{
			toolCall: cloneExecution(toolCall),
		},
	)
	if err != nil {
		return preDispatchEvaluation{}, err
	}
	if ask, needsApproval := gateOutcome.Decision.(AskDecision); needsApproval {
		return preDispatchEvaluation{
			approval: &ApprovalRequest{
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
	toolCall ToolExecution,
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

func approvalDecision(name string, decisionOutcome approval.Outcome) (PreToolDecision, bool, error) {
	switch decisionOutcome {
	case approval.OutcomeAllowedOnce:
		return AllowDecision{}, false, nil
	case approval.OutcomeRejected:
		return DenyDecision{
			Reason: fmt.Sprintf(
				"the user rejected tool %q",
				name,
			),
		}, false, nil
	case approval.OutcomeCancelled:
		return DenyDecision{
			Reason: fmt.Sprintf(
				"approval for tool %q was cancelled",
				name,
			),
		}, true, nil
	case approval.OutcomeUnavailable:
		return DenyDecision{
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

func fallbackAskReason(name string, reason string) string {
	if strings.TrimSpace(reason) != "" {
		return reason
	}
	return fmt.Sprintf("tool %q requires approval (not yet supported)", name)
}

func decisionDenial(name string, decision PreToolDecision) string {
	switch selected := decision.(type) {
	case AllowDecision:
		return ""
	case DenyDecision:
		return selected.Reason
	case AskDecision:
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

func deniedResult(reason string) ToolExecutionResult {
	return &ToolExecutionFailure{
		Error: ToolFailure{
			Message: reason,
		},
		Content: []agentmessage.ContentBlock{
			agentmessage.NewTextBlock("Error: " + reason),
		},
	}
}

func invokeGuard(
	policy ToolGuard,
	toolCall ToolExecution,
) (reason string, denied bool, invokeErr error) {
	defer func() {
		if panicValue := recover(); panicValue != nil {
			invokeErr = fmt.Errorf(
				"tools: guard panicked: %v",
				panicValue,
			)
		}
	}()
	reason, denied = policy.DenyReason(cloneExecution(toolCall))
	return reason, denied, nil
}
