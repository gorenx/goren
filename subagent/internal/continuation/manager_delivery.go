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

// Followup accepts one later FIFO message, cold-resuming when necessary.
func (owner *Manager) Followup(
	requestContext context.Context,
	parentAgent agent.Agent,
	childID session.SessionID,
	content []llm.ContentBlock,
	options subagent.FollowupOptions,
) (llm.MessageID, error) {
	if contextErr := checkContext(requestContext, "continuable Followup"); contextErr != nil {
		return "", contextErr
	}
	contentSnapshot, cloneErr := llm.CloneContentBlocks(content)
	if cloneErr != nil {
		return "", cloneErr
	}
	if options.Source == nil {
		return "", errors.New("subagent: Followup MessageSource is required")
	}
	sourceSnapshot, cloneErr := options.Source.CloneSource()
	if cloneErr != nil {
		return "", cloneErr
	}
	childMutex := owner.lockFor(childID)
	childMutex.Lock()
	defer childMutex.Unlock()
	if admissionErr := owner.assertAdmitting(parentAgent); admissionErr != nil {
		return "", admissionErr
	}
	owner.activations.mutex.Lock()
	epoch := owner.activations.activations[childID]
	materialized := false
	if epoch != nil && epoch.disposal != nil {
		done := epoch.disposal.done
		owner.activations.mutex.Unlock()
		if done != nil {
			select {
			case <-requestContext.Done():
				return "", requestContext.Err()
			case <-done:
			}
		}
		epoch = nil
	} else {
		owner.activations.mutex.Unlock()
	}
	if epoch == nil {
		resumed, resumeErr := owner.resume(
			requestContext,
			parentAgent,
			childID,
		)
		if resumeErr != nil {
			return "", resumeErr
		}
		epoch = resumed
		materialized = true
	}
	messageID, submitErr := owner.submit(
		requestContext,
		epoch,
		parentAgent,
		contentSnapshot,
		sourceSnapshot,
	)
	if submitErr != nil && epoch != nil && !epoch.announced {
		_ = owner.dispose(context.Background(), epoch, subagent.StopAborted)
	}
	if submitErr == nil && materialized {
		owner.watch(epoch)
	}
	return messageID, submitErr
}

// Interrupt authorizes and signals a resident child without waiting for idle.
func (owner *Manager) Interrupt(
	targetID session.SessionID,
	authority subagent.InterruptAuthority,
) error {
	owner.activations.mutex.Lock()
	epoch := owner.activations.activations[targetID]
	owner.activations.mutex.Unlock()
	switch evidence := authority.(type) {
	case subagent.AncestorInterruptAuthority:
		if evidence.Agent == nil ||
			!owner.dependencies.Agents.Contains(evidence.Agent) ||
			evidence.Agent.ID() == targetID {
			return unauthorized(
				fmt.Sprintf("interrupting %q requires the exact live ancestor Agent", targetID),
			)
		}
		if epoch == nil {
			return nil
		}
		if !owner.isLiveAncestor(epoch.handle.Subject, evidence.Agent) {
			return unauthorized(
				fmt.Sprintf("subagent %q is not a live descendant of Agent %q", targetID, evidence.Agent.ID()),
			)
		}
	case subagent.UserInterruptAuthority:
		if epoch == nil {
			return nil
		}
		if epoch.parentID != evidence.ParentSessionID {
			return unauthorized(
				fmt.Sprintf("subagent %q belongs to another parent Session", targetID),
			)
		}
	default:
		return unauthorized("subagent interrupt authority is invalid")
	}
	owner.activations.mutex.Lock()
	closing := epoch.disposal != nil
	owner.activations.mutex.Unlock()
	if !closing {
		epoch.handle.Subject.Cancel(
			agent.ParentCancel{},
			agent.CancelOptions{
				KeepInbox: true,
			},
		)
	}
	return nil
}

// ReportFrom delivers selected content from an exact resident child to its
// live direct parent.
func (owner *Manager) ReportFrom(
	requestContext context.Context,
	childAgent agent.Agent,
	content []llm.ContentBlock,
	options subagent.ReportOptions,
) (llm.MessageID, error) {
	if contextErr := checkContext(requestContext, "continuable ReportFrom"); contextErr != nil {
		return "", contextErr
	}
	if admissionErr := owner.assertAdmitting(childAgent); admissionErr != nil {
		return "", admissionErr
	}
	owner.activations.mutex.Lock()
	epoch := owner.activations.activations[childAgent.ID()]
	closing := epoch != nil && epoch.disposal != nil
	owner.activations.mutex.Unlock()
	if epoch == nil || !agent.Same(epoch.handle.Subject, childAgent) {
		return "", unauthorized(
			fmt.Sprintf("Agent %q is not a live continuable subagent", childAgent.ID()),
		)
	}
	if closing {
		return "", &subagent.Error{
			Code:    subagent.ErrorActivationClosing,
			Message: fmt.Sprintf("subagent %q Activation is closing", childAgent.ID()),
		}
	}
	parentAgent, found := owner.dependencies.Agents.Get(epoch.parentID)
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
				fmt.Sprintf("Background subagent %s reported:", epoch.childID),
			),
		},
		contentSnapshot...,
	)
	messageValue, messageErr := llm.NewUserMessage(llm.UserMessageInput{
		Content: framed,
		Source: subagent.ReportSource{
			SenderSessionID: epoch.childID,
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

func (owner *Manager) submit(
	requestContext context.Context,
	epoch *Activation,
	parentAgent agent.Agent,
	content []llm.ContentBlock,
	source llm.MessageSource,
) (llm.MessageID, error) {
	if requestErr := requestContext.Err(); requestErr != nil {
		return "", requestErr
	}
	if !owner.dependencies.Agents.Contains(parentAgent) ||
		epoch.parentID != parentAgent.ID() {
		return "", unauthorized(
			fmt.Sprintf("subagent %q delivery requires the exact live parent Agent", epoch.childID),
		)
	}
	messageValue, messageErr := llm.NewUserMessage(llm.UserMessageInput{
		Content: content,
		Source:  source,
	})
	if messageErr != nil {
		return "", messageErr
	}
	owner.activations.mutex.Lock()
	if epoch.disposal != nil {
		owner.activations.mutex.Unlock()
		return "", &subagent.Error{
			Code:    subagent.ErrorActivationClosing,
			Message: fmt.Sprintf("subagent %q Activation is closing", epoch.childID),
		}
	}
	if owner.activations.admission == activationsClosing {
		owner.activations.mutex.Unlock()
		return "", &subagent.Error{
			Code:    subagent.ErrorDraining,
			Message: "continuable subagents are closing; the message was not accepted",
		}
	}
	epoch.accepted[messageValue.StableID()] = struct{}{}
	wake(epoch)
	owner.activations.mutex.Unlock()
	if sendErr := epoch.handle.Subject.Followup(messageValue); sendErr != nil {
		owner.activations.mutex.Lock()
		delete(epoch.accepted, messageValue.StableID())
		owner.activations.mutex.Unlock()
		return "", sendErr
	}
	owner.activations.mutex.Lock()
	epoch.announced = true
	owner.activations.mutex.Unlock()
	return messageValue.StableID(), nil
}

func (owner *Manager) isLiveAncestor(
	childAgent agent.Agent,
	candidate agent.Agent,
) bool {
	if childAgent == nil || candidate == nil {
		return false
	}
	seen := make(map[session.SessionID]struct{})
	parentID := childAgent.SessionValue().Header().ParentSession
	for parentID != nil {
		if _, duplicate := seen[*parentID]; duplicate {
			return false
		}
		seen[*parentID] = struct{}{}
		ancestor, found := owner.dependencies.Agents.Get(*parentID)
		if !found {
			return false
		}
		if agent.Same(ancestor, candidate) {
			return true
		}
		parentID = ancestor.SessionValue().Header().ParentSession
	}
	return false
}
