package continuable

import (
	"context"
	"errors"
	"fmt"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

// Send accepts one later FIFO message, cold-resuming when necessary.
func (owner *Service) Send(
	requestContext context.Context,
	parentAgent agent.Agent,
	childID session.SessionID,
	content []llm.ContentBlock,
	options subagent.FollowupOptions,
) (llm.MessageID, error) {
	if contextErr := checkContext(requestContext, "Continuable Send"); contextErr != nil {
		return "", contextErr
	}
	contentSnapshot, cloneErr := llm.CloneContentBlocks(content)
	if cloneErr != nil {
		return "", cloneErr
	}
	if options.Source == nil {
		return "", errors.New("subagent: Send MessageSource is required")
	}
	sourceSnapshot, cloneErr := options.Source.CloneSource()
	if cloneErr != nil {
		return "", cloneErr
	}
	for {
		slot := owner.acquireSlot(childID)
		slot.mutex.Lock()
		if authorizationErr := owner.authorizeParent(parentAgent); authorizationErr != nil {
			slot.mutex.Unlock()
			owner.releaseSlot(childID, slot)
			return "", authorizationErr
		}
		current := slot.current
		if current != nil && current.running.State() != subagent.ExecutionActive {
			running := current.running
			slot.mutex.Unlock()
			owner.releaseSlot(childID, slot)
			if waitErr := running.Wait(requestContext); waitErr != nil {
				return "", waitErr
			}
			continue
		}
		messageValue, messageErr := llm.NewUserMessage(llm.UserMessageInput{
			Content: contentSnapshot,
			Source:  sourceSnapshot,
		})
		if messageErr != nil {
			slot.mutex.Unlock()
			owner.releaseSlot(childID, slot)
			return "", messageErr
		}
		if current == nil {
			handle, seedBuilder, resumeErr := owner.resume(
				requestContext,
				parentAgent,
				childID,
			)
			if resumeErr != nil {
				slot.mutex.Unlock()
				owner.releaseSlot(childID, slot)
				return "", resumeErr
			}
			if submitErr := handle.Subject.Followup(messageValue); submitErr != nil {
				slot.mutex.Unlock()
				owner.releaseSlot(childID, slot)
				return "", errors.Join(
					submitErr,
					handle.Dispose(context.WithoutCancel(requestContext)),
				)
			}
			current, resumeErr = owner.publish(
				handle,
				parentAgent,
				seedBuilder,
				slot,
			)
			if resumeErr != nil {
				slot.mutex.Unlock()
				owner.releaseSlot(childID, slot)
				return "", errors.Join(
					resumeErr,
					handle.Dispose(context.WithoutCancel(requestContext)),
				)
			}
			slot.current = current
			owner.watch(current)
		} else {
			if current.terminator.parent.ID() != parentAgent.ID() ||
				!owner.dependencies.Agents.Contains(parentAgent) {
				slot.mutex.Unlock()
				owner.releaseSlot(childID, slot)
				return "", unauthorized(
					fmt.Sprintf(
						"subagent %q delivery requires its exact live parent",
						childID,
					),
				)
			}
			if submitErr := current.terminator.handle.Subject.Followup(
				messageValue,
			); submitErr != nil {
				slot.mutex.Unlock()
				owner.releaseSlot(childID, slot)
				return "", submitErr
			}
			signal(current)
		}
		slot.mutex.Unlock()
		owner.releaseSlot(childID, slot)
		return messageValue.StableID(), nil
	}
}

// Interrupt cancels the current turn while retaining pending Inbox messages
// and the durable child Session.
func (owner *Service) Interrupt(
	requestContext context.Context,
	targetID session.SessionID,
) error {
	if contextErr := checkContext(
		requestContext,
		"Continuable Interrupt",
	); contextErr != nil {
		return contextErr
	}
	owner.mutex.Lock()
	slot := owner.slots[targetID]
	owner.mutex.Unlock()
	if slot == nil {
		return nil
	}
	slot.mutex.Lock()
	current := slot.current
	if current == nil {
		slot.mutex.Unlock()
		return nil
	}
	childAgent := current.terminator.handle.Subject
	if current.running.State() == subagent.ExecutionActive {
		childAgent.Cancel(
			agent.ParentCancel{},
			agent.CancelOptions{
				KeepInbox: true,
			},
		)
	}
	slot.mutex.Unlock()
	return nil
}

// Report delivers child content to its exact live direct parent.
func (owner *Service) Report(
	requestContext context.Context,
	childAgent agent.Agent,
	content []llm.ContentBlock,
	options subagent.ReportOptions,
) (llm.MessageID, error) {
	if contextErr := checkContext(requestContext, "Continuable Report"); contextErr != nil {
		return "", contextErr
	}
	if childAgent == nil {
		return "", unauthorized("report requires an exact child Agent")
	}
	owner.mutex.Lock()
	slot := owner.slots[childAgent.ID()]
	owner.mutex.Unlock()
	if slot == nil {
		return "", unauthorized(
			fmt.Sprintf("Agent %q is not a live Subagent", childAgent.ID()),
		)
	}
	slot.mutex.Lock()
	current := slot.current
	if current == nil ||
		!agent.Same(current.terminator.handle.Subject, childAgent) ||
		current.running.State() != subagent.ExecutionActive {
		slot.mutex.Unlock()
		return "", unauthorized(
			fmt.Sprintf("Agent %q is not an active Subagent", childAgent.ID()),
		)
	}
	parentAgent, found := owner.dependencies.Agents.Get(
		current.terminator.parent.ID(),
	)
	slot.mutex.Unlock()
	if !found {
		return "", &subagent.Error{
			Code:    subagent.ErrorParentUnavailable,
			Message: "direct parent is not live; report was not delivered",
		}
	}
	contentSnapshot, cloneErr := llm.CloneContentBlocks(content)
	if cloneErr != nil {
		return "", cloneErr
	}
	framed := append(
		[]llm.ContentBlock{
			llm.NewTextBlock(
				fmt.Sprintf("Background subagent %s reported:", childAgent.ID()),
			),
		},
		contentSnapshot...,
	)
	messageValue, messageErr := llm.NewUserMessage(llm.UserMessageInput{
		Content: framed,
		Source: subagent.ReportSource{
			SenderSessionID: childAgent.ID(),
		},
	})
	if messageErr != nil {
		return "", messageErr
	}
	switch options.Delivery {
	case subagent.ReportQuiet:
		messageErr = parentAgent.Inject(messageValue)
	case subagent.ReportNextStep:
		messageErr = parentAgent.Steer(messageValue)
	default:
		return "", errors.New("subagent: unsupported report delivery")
	}
	if messageErr != nil {
		return "", &subagent.Error{
			Code:    subagent.ErrorParentUnavailable,
			Message: "direct parent is not live; report was not delivered",
			Cause:   messageErr,
		}
	}
	return messageValue.StableID(), nil
}

var _ subagent.ParentReporter = (*Service)(nil)
