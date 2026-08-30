package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/approval"
)

type policyEngine struct {
	effects   layerEffects
	registry  *registry
	approvals approval.Approval
}

type preDispatchEvaluation struct {
	denialReason      string
	approvalCancelled bool
}

func (engine *policyEngine) evaluate(
	requestContext context.Context,
	toolCall ToolExecution,
) (preDispatchEvaluation, error) {
	gateOutcome, err := engine.effects.ResolvePreExecute(
		requestContext,
		PreExecuteRequest{
			toolCall: toolCall,
		},
	)
	if err != nil {
		return preDispatchEvaluation{}, err
	}
	resolvedGate, approvalCancelled, err := engine.resolveAsk(
		requestContext,
		toolCall,
		gateOutcome.Decision,
	)
	if err != nil {
		return preDispatchEvaluation{}, err
	}
	denialReason := decisionDenial(toolCall.Name, resolvedGate)
	if denialReason == "" {
		denialReason, err = engine.guardDenial(toolCall)
		if err != nil {
			return preDispatchEvaluation{}, err
		}
	}
	return preDispatchEvaluation{
		denialReason:      denialReason,
		approvalCancelled: approvalCancelled,
	}, nil
}

type preExecuteTerminal struct{}

func (preExecuteTerminal) Execute(
	context.Context,
	PreExecuteRequest,
) (PreExecuteOutcome, error) {
	return PreExecuteOutcome{
		Decision: AllowDecision{},
	}, nil
}

func (engine *policyEngine) guardDenial(
	toolCall ToolExecution,
) (string, error) {
	for _, policy := range engine.registry.guards() {
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

func (engine *policyEngine) resolveAsk(
	requestContext context.Context,
	toolCall ToolExecution,
	gate PreToolDecision,
) (PreToolDecision, bool, error) {
	ask, needsApproval := gate.(AskDecision)
	if !needsApproval {
		return gate, false, nil
	}
	if engine.approvals == nil {
		return DenyDecision{
			Reason: fallbackAskReason(toolCall.Name, ask.Reason),
		}, false, nil
	}
	if toolCall.Subject == nil {
		return DenyDecision{
			Reason: fmt.Sprintf(
				"tool %q requires approval, but the call has no subject",
				toolCall.Name,
			),
		}, false, nil
	}
	callID := toolCall.CallID
	decisionRequest := approval.Request{
		Subject:  toolCall.Subject,
		ToolName: toolCall.Name,
		CallID:   &callID,
	}
	if strings.TrimSpace(ask.Reason) != "" {
		reason := ask.Reason
		decisionRequest.Reason = &reason
	}
	decisionOutcome, err := engine.approvals.Request(
		requestContext,
		decisionRequest,
	)
	if err != nil {
		return nil, false, err
	}
	switch decisionOutcome {
	case approval.OutcomeAllowedOnce:
		return AllowDecision{}, false, nil
	case approval.OutcomeRejected:
		return DenyDecision{
			Reason: fmt.Sprintf(
				"the user rejected tool %q",
				toolCall.Name,
			),
		}, false, nil
	case approval.OutcomeCancelled:
		return DenyDecision{
			Reason: fmt.Sprintf(
				"approval for tool %q was cancelled",
				toolCall.Name,
			),
		}, true, nil
	case approval.OutcomeUnavailable:
		return DenyDecision{
			Reason: fmt.Sprintf(
				"tool %q requires approval, but no approval channel is available",
				toolCall.Name,
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
