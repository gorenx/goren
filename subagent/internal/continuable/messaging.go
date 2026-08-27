package continuable

import (
	"context"
	"errors"
	"fmt"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

// Resume materializes a durable child and atomically accepts the first message
// of the new Agent epoch. If another caller made the child resident first, the
// same slot serializes delivery to that exact Agent.
func (owner *Service) Resume(
	requestContext context.Context,
	parentAgent agent.Agent,
	childID session.SessionID,
	messageValue agentmessage.UserMessage,
) (agentmessage.MessageID, error) {
	if contextErr := checkContext(requestContext, "Continuable Resume"); contextErr != nil {
		return "", contextErr
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
