package continuable

import (
	"fmt"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

func (terminator *executionTerminator) notifyParent(
	terminalValue subagent.Terminal,
) {
	parentAgent, found := terminator.owner.dependencies.Agents.Get(
		terminator.parent.ID(),
	)
	if !found {
		return
	}
	summary := settlementSummary(
		terminator.handle.Subject.ID(),
		terminalValue.StopReason,
	)
	content := []agentmessage.ContentBlock{
		agentmessage.NewTextBlock(summary),
	}
	if len(terminalValue.Output) == 0 {
		content = append(content, agentmessage.NewTextBlock("It left no closing message."))
	} else {
		content = append(content, agentmessage.NewTextBlock("Its closing message:"))
		content = append(content, terminalValue.Output...)
	}
	messageValue, messageErr := agentmessage.NewUserMessage(
		agentmessage.UserMessageInput{
			Content: content,
			Source: subagent.SettlementSource{
				Summary:         summary,
				SenderSessionID: terminator.handle.Subject.ID(),
			},
		},
	)
	if messageErr != nil {
		return
	}
	if parentAgent.StatusValue() == agent.StatusIdle {
		_ = parentAgent.Followup(messageValue)
	} else {
		_ = parentAgent.Steer(messageValue)
	}
}

func settlementSummary(
	childID session.SessionID,
	reason subagent.StopReason,
) string {
	subject := fmt.Sprintf("Background subagent %s", childID)
	switch reason {
	case subagent.StopCompleted:
		return subject + " finished and can be resumed by another message."
	case subagent.StopAborted:
		return subject + " was stopped before it finished."
	case subagent.StopMaxTokens:
		return subject + " ran out of room before it finished."
	case subagent.StopRefusal:
		return subject + " declined the task."
	default:
		return subject + " failed before it finished."
	}
}
