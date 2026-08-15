package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/gorenx/goren/approval"
)

func (owner *toolRegistry) resolveAsk(
	requestContext context.Context,
	execution ToolExecution,
	gate PreToolDecision,
) (PreToolDecision, bool, error) {
	ask, needsApproval := gate.(AskDecision)
	if !needsApproval {
		return gate, false, nil
	}
	if owner.approvals == nil {
		return DenyDecision{Reason: fallbackAskReason(execution.Name, ask.Reason)}, false, nil
	}
	approvalService, found := owner.approvals.ResolveApproval()
	if !found || approvalService == nil {
		return DenyDecision{Reason: fallbackAskReason(execution.Name, ask.Reason)}, false, nil
	}
	if execution.Subject == nil {
		return DenyDecision{Reason: fmt.Sprintf(
			"tool %q requires approval, but the call has no agent to route it through", execution.Name,
		)}, false, nil
	}
	callID := execution.CallID
	decisionRequest := approval.Request{
		Subject: execution.Subject, ToolName: execution.Name, CallID: &callID,
	}
	if strings.TrimSpace(ask.Reason) != "" {
		reason := ask.Reason
		decisionRequest.Reason = &reason
	}
	decisionOutcome, err := approvalService.Request(requestContext, decisionRequest)
	if err != nil {
		return nil, false, err
	}
	switch decisionOutcome {
	case approval.OutcomeAllowedOnce:
		return AllowDecision{}, false, nil
	case approval.OutcomeRejected:
		return DenyDecision{Reason: fmt.Sprintf("the user rejected tool %q", execution.Name)}, false, nil
	case approval.OutcomeCancelled:
		return DenyDecision{Reason: fmt.Sprintf("approval for tool %q was cancelled", execution.Name)}, true, nil
	case approval.OutcomeUnavailable:
		return DenyDecision{Reason: fmt.Sprintf(
			"tool %q requires approval, but no approval channel is available", execution.Name,
		)}, false, nil
	default:
		return nil, false, fmt.Errorf("tools: unsupported ApprovalOutcome %q", decisionOutcome)
	}
}

func fallbackAskReason(name string, reason string) string {
	if strings.TrimSpace(reason) != "" {
		return reason
	}
	return fmt.Sprintf("tool %q requires approval (not yet supported)", name)
}
