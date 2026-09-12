package companionactor

import (
	"context"
	"errors"

	agentregistry "github.com/gorenx/goren/agent/registry"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	companions "github.com/gorenx/goren/subagent/companion"
)

type sendMessage struct {
	// ctx carries the caller's cancellation and deadline for this ordinary delivery.
	ctx context.Context
	// child is the direct Companion SessionID whose current lifetime Steward resolves.
	child session.SessionID
	// message already has a stable ID and coordinator provenance.
	message agentmessage.UserMessage
}

type reportMessage struct {
	// ctx bounds report delivery; canceling the wait does not retract committed input.
	ctx context.Context
	// child is the exact reporting lifetime whose direct parent relationship Steward verifies.
	child agentregistry.Creation
	// message contains the report with a stable ID and subagent-report provenance.
	message agentmessage.UserMessage
}

// SendMessage queues the next turn of an available direct Companion. Its result
// confirms ordinary Inbox acceptance, not execution completion or Session Flush.
func (sender *Client) SendMessage(ctx context.Context, child session.SessionID, content []agentmessage.ContentBlock) (agentmessage.MessageID, error) {
	if sender == nil || ctx == nil || child == "" {
		return "", companions.ErrInvalid
	}
	message, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
		Content: content,
		Source: subagent.CoordinatorSource{
			SenderSessionID: sender.parent.AgentID,
		},
	})
	if err != nil {
		return "", err
	}
	result, err := sender.request(ctx, sendMessage{
		ctx:     ctx,
		child:   child,
		message: message,
	})
	return result.messageID, errors.Join(err, result.err)
}

// Report queues child output for the parent's next step or turn without waking it.
// The caller supplies a trusted child Creation; the Steward verifies membership.
func (sender *Client) Report(ctx context.Context, child agentregistry.Creation, output string) (agentmessage.MessageID, error) {
	if sender == nil || ctx == nil || child.AgentID == "" || child.CreationID == "" {
		return "", companions.ErrInvalid
	}
	message, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
		Content: []agentmessage.ContentBlock{agentmessage.NewTextBlock(output)},
		Source: subagent.ReportSource{
			SenderSessionID: child.AgentID,
		},
	})
	if err != nil {
		return "", err
	}
	result, err := sender.request(ctx, reportMessage{
		ctx:     ctx,
		child:   child,
		message: message,
	})
	return result.messageID, errors.Join(err, result.err)
}
