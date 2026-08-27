package apiproxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/approval"
	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/plugin"
)

type pendingApproval struct {
	rpcID      connection.RPCID
	sessionID  SessionID
	requestID  approval.RequestID
	toolName   string
	callID     *agentmessage.CallID
	reason     *string
	waiting    *PendingResponse[approval.Outcome]
	settlement interactionSettlement
}

// ResolveApproval implements the API Proxy Plugin's approval Waterfall step.
func (owner *InteractionGateway) ResolveApproval(
	requestContext context.Context,
	input approval.DecisionRequest,
	downstream plugin.WaterfallAction[approval.DecisionRequest, approval.Decision],
) (approval.Decision, error) {
	decisionRequest := input.Request
	if requestContext.Err() != nil {
		return approval.Decision{
			Outcome: approval.OutcomeCancelled,
		}, nil
	}
	if decisionRequest.Subject == nil || decisionRequest.Subject.SessionValue() == nil {
		return downstream.Execute(requestContext, input)
	}

	owner.mu.Lock()
	if owner.closed {
		owner.mu.Unlock()
		return approval.Decision{
			Outcome: approval.OutcomeCancelled,
		}, nil
	}
	requestID, found, err := owner.unclaimedApprovalID(decisionRequest)
	if err != nil {
		owner.mu.Unlock()
		return approval.Decision{}, err
	}
	if !found {
		owner.mu.Unlock()
		return downstream.Execute(requestContext, input)
	}
	correlationID, err := owner.newRPC()
	if err != nil {
		owner.mu.Unlock()
		return approval.Decision{}, fmt.Errorf("apiproxy: mint approval rpcId: %w", err)
	}
	conversationID := SessionID(decisionRequest.Subject.SessionValue().ID())
	waiting, err := RegisterPendingResponse(owner.methods, correlationID,
		func(result connection.RPCResult) (approval.Outcome, bool) {
			return decodeApprovalResponse(result, conversationID, requestID)
		})
	if err != nil {
		owner.mu.Unlock()
		return approval.Decision{}, err
	}
	entry := &pendingApproval{
		rpcID: correlationID, sessionID: conversationID, requestID: requestID,
		toolName: decisionRequest.ToolName, callID: cloneCallID(decisionRequest.CallID),
		reason: cloneInteractionString(decisionRequest.Reason), waiting: waiting,
		settlement: newInteractionSettlement(),
	}
	owner.approvals[correlationID] = entry
	publishErr := owner.frames.PublishPending(correlationID, entry.requestedFrame())
	if publishErr != nil {
		delete(owner.approvals, correlationID)
		waiting.Withdraw(publishErr)
		entry.settlement.complete(nil)
		owner.mu.Unlock()
		return approval.Decision{}, publishErr
	}
	owner.mu.Unlock()

	decisionOutcome, waitErr := waiting.Wait(requestContext)
	if waitErr != nil {
		if errors.Is(context.Cause(requestContext), plugin.ErrPluginNotActive) {
			<-entry.settlement.done
			return approval.Decision{
				Outcome: approval.OutcomeCancelled,
			}, nil
		}
		entry.finish(owner, approval.OutcomeCancelled)
		if errors.Is(waitErr, context.Canceled) || errors.Is(waitErr, context.DeadlineExceeded) ||
			errors.Is(waitErr, errInteractionGatewayClosed) {
			return approval.Decision{
				Outcome: approval.OutcomeCancelled,
			}, nil
		}
		return approval.Decision{}, waitErr
	}
	entry.finish(owner, decisionOutcome)
	return approval.Decision{
		Outcome: decisionOutcome,
	}, nil
}

func (owner *InteractionGateway) unclaimedApprovalID(
	decisionRequest approval.Request,
) (approval.RequestID, bool, error) {
	claimed := make(map[approval.RequestID]struct{}, len(owner.approvals))
	for _, entry := range owner.approvals {
		claimed[entry.requestID] = struct{}{}
	}
	decided := make(map[approval.RequestID]struct{})
	entries := decisionRequest.Subject.SessionValue().Events()
	for index := len(entries) - 1; index >= 0; index-- {
		committed := entries[index]
		switch committed.Type {
		case approval.DecidedEventName:
			var terminal approval.Decided
			if err := json.Unmarshal(committed.Data, &terminal); err != nil {
				return "", false, fmt.Errorf("apiproxy: decode approval/decided at seq %d: %w", committed.Seq, err)
			}
			decided[terminal.ID] = struct{}{}
		case approval.AskedEventName:
			var askedValue approval.Asked
			if err := json.Unmarshal(committed.Data, &askedValue); err != nil {
				return "", false, fmt.Errorf("apiproxy: decode approval/asked at seq %d: %w", committed.Seq, err)
			}
			if _, exists := decided[askedValue.ID]; exists {
				continue
			}
			if _, exists := claimed[askedValue.ID]; exists {
				continue
			}
			if !sameCallID(decisionRequest.CallID, askedValue.CallID) {
				continue
			}
			return askedValue.ID, true, nil
		}
	}
	return "", false, nil
}

func (entry *pendingApproval) requestedFrame() ApprovalRequestedFrame {
	frameValue := ApprovalRequestedFrame{
		SessionID: entry.sessionID, ApprovalID: ApprovalRequestID(entry.requestID),
		ToolName: entry.toolName, Reason: cloneInteractionString(entry.reason),
	}
	if entry.callID != nil {
		callText := string(*entry.callID)
		frameValue.CallID = &callText
	}
	return frameValue
}

func (entry *pendingApproval) finish(owner *InteractionGateway, decisionOutcome approval.Outcome) {
	entry.settlement.complete(func() {
		owner.mu.Lock()
		if owner.approvals[entry.rpcID] == entry {
			delete(owner.approvals, entry.rpcID)
		}
		owner.mu.Unlock()
		owner.report(owner.frames.ResolvePending(entry.rpcID, ApprovalResolvedFrame{
			SessionID: entry.sessionID, ApprovalID: ApprovalRequestID(entry.requestID),
			Outcome: ApprovalOutcome(decisionOutcome),
		}))
	})
}

func decodeApprovalResponse(
	result connection.RPCResult,
	expectedSessionID SessionID,
	expectedRequestID approval.RequestID,
) (approval.Outcome, bool) {
	if !result.OK || result.Error != nil {
		return "", false
	}
	var wireValue struct {
		SessionID  *SessionID         `json:"sessionId"`
		ApprovalID *ApprovalRequestID `json:"approvalId"`
		Outcome    *ApprovalOutcome   `json:"outcome"`
	}
	if err := json.Unmarshal(result.Value, &wireValue); err != nil || wireValue.SessionID == nil ||
		wireValue.ApprovalID == nil || wireValue.Outcome == nil {
		return "", false
	}
	if *wireValue.SessionID != expectedSessionID ||
		*wireValue.ApprovalID != ApprovalRequestID(expectedRequestID) {
		return "", false
	}
	switch *wireValue.Outcome {
	case ApprovalAllowedOnce:
		return approval.OutcomeAllowedOnce, true
	case ApprovalRejected:
		return approval.OutcomeRejected, true
	default:
		return "", false
	}
}

func sameCallID(leftValue *agentmessage.CallID, rightValue *agentmessage.CallID) bool {
	if leftValue == nil || rightValue == nil {
		return leftValue == nil && rightValue == nil
	}
	return *leftValue == *rightValue
}

func cloneCallID(source *agentmessage.CallID) *agentmessage.CallID {
	if source == nil {
		return nil
	}
	snapshot := *source
	return &snapshot
}

func cloneInteractionString(source *string) *string {
	if source == nil {
		return nil
	}
	snapshot := *source
	return &snapshot
}
