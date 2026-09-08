package interaction

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	rpc "github.com/gorenx/goren/apiproxy/rpc"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/approval"
	"github.com/gorenx/goren/connection"
)

type pendingApproval struct {
	rpcID      connection.RPCID
	sessionID  SessionID
	requestID  approval.RequestID
	toolName   string
	callID     *agentmessage.CallID
	reason     *string
	waiting    *rpc.PendingResponse[approval.Outcome]
	settlement interactionSettlement
}

// Answer routes an audited approval question to the client and settles its frames.
func (owner *Gateway) Answer(ctx context.Context, input approval.Question) (approval.Outcome, error) {
	if ctx == nil {
		return "", errors.New("apiproxy: approval context is required")
	}
	if ctx.Err() != nil {
		return approval.OutcomeCancelled, nil
	}
	if input.ID == "" || input.SessionID == "" || input.ToolName == "" {
		return "", errors.New("apiproxy: approval ID, SessionID and tool name are required")
	}
	owner.mu.Lock()
	if owner.closeDone != nil {
		owner.mu.Unlock()
		return approval.OutcomeCancelled, nil
	}
	for _, pending := range owner.approvals {
		if pending.requestID == input.ID && pending.sessionID == SessionID(input.SessionID) {
			owner.mu.Unlock()
			return "", errors.New("apiproxy: approval request is already pending")
		}
	}
	correlationID, err := owner.newRPC()
	if err != nil {
		owner.mu.Unlock()
		return "", fmt.Errorf("apiproxy: mint approval rpcId: %w", err)
	}
	conversationID := SessionID(input.SessionID)
	waiting, err := rpc.RegisterPendingResponse(owner.methods, correlationID,
		func(result connection.RPCResult) (approval.Outcome, bool) {
			return decodeApprovalResponse(result, conversationID, input.ID)
		})
	if err != nil {
		owner.mu.Unlock()
		return "", err
	}
	entry := &pendingApproval{
		rpcID:      correlationID,
		sessionID:  conversationID,
		requestID:  input.ID,
		toolName:   input.ToolName,
		callID:     cloneCallID(input.CallID),
		reason:     cloneInteractionString(input.Reason),
		waiting:    waiting,
		settlement: newInteractionSettlement(),
	}
	owner.approvals[correlationID] = entry
	if err := owner.frames.PublishPending(correlationID, entry.requestedFrame()); err != nil {
		delete(owner.approvals, correlationID)
		waiting.Withdraw(err)
		entry.settlement.complete(nil)
		owner.mu.Unlock()
		return "", err
	}
	owner.mu.Unlock()
	outcome, err := waiting.Wait(ctx)
	if err != nil {
		entry.finish(owner, approval.OutcomeCancelled)
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errInteractionGatewayClosed) {
			return approval.OutcomeCancelled, nil
		}
		return "", err
	}
	entry.finish(owner, outcome)
	return outcome, nil
}

var _ approval.Answerer = (*Gateway)(nil)

func (entry *pendingApproval) requestedFrame() ApprovalRequestedFrame {
	frameValue := ApprovalRequestedFrame{
		SessionID:  entry.sessionID,
		ApprovalID: ApprovalRequestID(entry.requestID),
		ToolName:   entry.toolName,
		Reason:     cloneInteractionString(entry.reason),
	}
	if entry.callID != nil {
		callText := string(*entry.callID)
		frameValue.CallID = &callText
	}
	return frameValue
}

func (entry *pendingApproval) finish(owner *Gateway, decisionOutcome approval.Outcome) {
	entry.settlement.complete(func() {
		owner.mu.Lock()
		if owner.approvals[entry.rpcID] == entry {
			delete(owner.approvals, entry.rpcID)
		}
		owner.mu.Unlock()
		owner.report(owner.frames.ResolvePending(entry.rpcID, ApprovalResolvedFrame{
			SessionID:  entry.sessionID,
			ApprovalID: ApprovalRequestID(entry.requestID),
			Outcome:    ApprovalOutcome(decisionOutcome),
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
