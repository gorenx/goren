package continuation

import (
	"context"
	"errors"
	"fmt"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

func (owner *Manager) watch(epoch *Activation) {
	go func() {
		for {
			owner.residency.mutex.Lock()
			if epoch.closing {
				owner.residency.mutex.Unlock()
				return
			}
			wakeSignal := epoch.wake
			owner.residency.mutex.Unlock()
			idleContext, cancelIdle := context.WithCancel(context.Background())
			idleResult := make(chan error, 1)
			go func() {
				idleResult <- epoch.handle.Subject.WhenIdle(idleContext)
			}()
			select {
			case <-wakeSignal:
				cancelIdle()
				<-idleResult
				continue
			case <-idleResult:
				cancelIdle()
			}
			owner.residency.mutex.Lock()
			settled := !epoch.closing && len(epoch.accepted) == 0 &&
				len(epoch.ownedChildren) == 0 &&
				epoch.handle.Subject.StatusValue() == agent.StatusIdle
			owner.residency.mutex.Unlock()
			if settled {
				_ = owner.dispose(context.Background(), epoch, subagent.StopCompleted)
				return
			}
		}
	}()
}

func (owner *Manager) dispose(
	requestContext context.Context,
	epoch *Activation,
	reason subagent.StopReason,
) error {
	owner.residency.mutex.Lock()
	if epoch.closing {
		done := epoch.disposeDone
		owner.residency.mutex.Unlock()
		if done != nil {
			select {
			case <-requestContext.Done():
				return requestContext.Err()
			case <-done:
			}
		}
		return epoch.disposeErr
	}
	epoch.closing = true
	epoch.terminalReason = reason
	epoch.disposeDone = make(chan struct{})
	wake(epoch)
	children := make([]*Activation, 0, len(epoch.ownedChildren))
	for childID := range epoch.ownedChildren {
		if childEpoch := owner.residency.activations[childID]; childEpoch != nil {
			children = append(children, childEpoch)
		}
	}
	owner.residency.mutex.Unlock()

	epoch.handle.Subject.Cancel(
		agent.ParentCancel{},
		agent.CancelOptions{
			KeepInbox: false,
		},
	)
	failures := make([]error, 0)
	for _, childEpoch := range children {
		if disposeErr := owner.dispose(requestContext, childEpoch, subagent.StopAborted); disposeErr != nil {
			failures = append(failures, disposeErr)
		}
	}
	if idleErr := epoch.handle.Subject.WhenIdle(requestContext); idleErr != nil {
		failures = append(failures, idleErr)
	}
	if owner.dependencies.Persistence != nil {
		if flushErr := owner.dependencies.Sessions.Flush(
			context.WithoutCancel(requestContext),
			epoch.handle.Subject.SessionValue(),
		); flushErr != nil {
			failures = append(failures, flushErr)
		}
	}
	lastOutput := lastAssistant(epoch.handle.Subject.SessionValue())
	if handleErr := epoch.handle.Dispose(context.WithoutCancel(requestContext)); handleErr != nil {
		failures = append(failures, handleErr)
	}

	owner.residency.mutex.Lock()
	epoch.disposeErr = errors.Join(failures...)
	owner.residency.mutex.Unlock()
	owner.notifySettlement(epoch, lastOutput)
	owner.residency.mutex.Lock()
	if owner.residency.activations[epoch.childID] == epoch {
		delete(owner.residency.activations, epoch.childID)
	}
	for _, candidate := range owner.residency.activations {
		if _, owned := candidate.ownedChildren[epoch.childID]; owned {
			delete(candidate.ownedChildren, epoch.childID)
			wake(candidate)
		}
	}
	close(epoch.disposeDone)
	owner.residency.mutex.Unlock()
	parentAgent, found := owner.dependencies.Agents.Get(epoch.parentID)
	if found && owner.dependencies.Lifecycle != nil {
		endReason := reason
		if epoch.disposeErr != nil {
			endReason = subagent.StopError
		}
		owner.dependencies.Lifecycle.Ended(
			parentAgent,
			subagent.Ended{
				RunID:                epoch.runID,
				Provider:             epoch.providerName,
				ID:                   epoch.childID,
				Local:                true,
				StopReason:           endReason,
				LastAssistantMessage: lastOutput,
			},
		)
	}
	return epoch.disposeErr
}

func (owner *Manager) notifySettlement(
	epoch *Activation,
	lastOutput []llm.ContentBlock,
) {
	if !epoch.announced {
		return
	}
	parentAgent, found := owner.dependencies.Agents.Get(epoch.parentID)
	if !found {
		return
	}
	summary := settlementSummary(epoch.childID, epoch.terminalReason)
	content := []llm.ContentBlock{
		llm.NewTextBlock(summary),
	}
	if len(lastOutput) == 0 {
		content = append(content, llm.NewTextBlock("It left no closing message."))
	} else {
		content = append(content, llm.NewTextBlock("Its closing message:"))
		content = append(content, lastOutput...)
	}
	messageValue, messageErr := llm.NewUserMessage(llm.UserMessageInput{
		Content: content,
		Source: subagent.SettlementSource{
			Summary:         summary,
			SenderSessionID: epoch.childID,
		},
	})
	if messageErr != nil {
		return
	}
	if parentAgent.StatusValue() == agent.StatusIdle {
		_ = parentAgent.Followup(messageValue)
	} else {
		_ = parentAgent.Steer(messageValue)
	}
}

func lastAssistant(conversation *session.Session) []llm.ContentBlock {
	messages, deriveErr := conversation.DeriveMessages()
	if deriveErr != nil {
		return nil
	}
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].ConversationRole() == llm.RoleAssistant {
			return messages[index].ContentValue()
		}
	}
	return nil
}

func settlementSummary(
	childID session.SessionID,
	reason subagent.StopReason,
) string {
	subject := fmt.Sprintf("Background subagent %s", childID)
	switch reason {
	case subagent.StopCompleted:
		return subject + " finished and will do no further work unless you send it more."
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

func wake(epoch *Activation) {
	select {
	case <-epoch.wake:
	default:
		close(epoch.wake)
	}
	epoch.wake = make(chan struct{})
}
