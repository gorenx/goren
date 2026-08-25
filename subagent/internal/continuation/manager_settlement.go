package continuation

import (
	"context"
	"fmt"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

func (owner *Manager) watch(epoch *Activation) {
	go func() {
		for {
			owner.activations.mutex.Lock()
			if epoch.disposal != nil {
				owner.activations.mutex.Unlock()
				return
			}
			wakeSignal := epoch.wake
			owner.activations.mutex.Unlock()
			idleContext, cancelIdle := context.WithCancel(context.Background())
			idleResult := make(chan error, 1)
			go func() {
				idleResult <- epoch.handle.Subject.WhenIdle(idleContext)
			}()
			select {
			case <-epoch.handle.ClosingSignal():
				cancelIdle()
				<-idleResult
				return
			case <-wakeSignal:
				cancelIdle()
				<-idleResult
				continue
			case <-idleResult:
				cancelIdle()
			}
			select {
			case <-epoch.handle.ClosingSignal():
				return
			default:
			}
			childMutex := owner.lockFor(epoch.childID)
			childMutex.Lock()
			childStatus := epoch.handle.Subject.StatusValue()
			owner.activations.mutex.Lock()
			settled := epoch.disposal == nil && len(epoch.accepted) == 0 &&
				childStatus == agent.StatusIdle
			owner.activations.mutex.Unlock()
			if settled {
				settled = !owner.dependencies.Descendants.HasRuntimeDescendants(
					epoch.handle.Subject,
				)
			}
			if !settled {
				childMutex.Unlock()
				continue
			}
			transaction, opened := owner.openDisposal(epoch)
			childMutex.Unlock()
			if opened {
				_ = owner.finishDisposal(
					context.Background(),
					epoch,
					transaction,
					subagent.StopCompleted,
				)
			}
			return
		}
	}()
}

func (owner *Manager) notifySettlement(
	epoch *Activation,
	lastOutput []llm.ContentBlock,
	reason subagent.StopReason,
) {
	if !epoch.announced {
		return
	}
	parentAgent, found := owner.dependencies.Agents.Get(epoch.parentID)
	if !found {
		return
	}
	summary := settlementSummary(epoch.childID, reason)
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
