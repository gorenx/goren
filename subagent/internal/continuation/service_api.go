package continuation

import (
	"context"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

// StartContinuable creates one durable continuable child.
func (owner *Service) StartContinuable(
	requestContext context.Context,
	startSpec subagent.ContinuableStartSpec,
) (subagent.ContinuableStart, error) {
	activeManager, err := owner.requireManager()
	if err != nil {
		return subagent.ContinuableStart{}, err
	}
	return activeManager.Start(requestContext, startSpec)
}

// Followup delivers one later FIFO turn to a resident or resumed child.
func (owner *Service) Followup(
	requestContext context.Context,
	parentAgent agent.Agent,
	childID session.SessionID,
	content []llm.ContentBlock,
	options subagent.FollowupOptions,
) (llm.MessageID, error) {
	activeManager, err := owner.requireManager()
	if err != nil {
		return "", err
	}
	return activeManager.Followup(
		requestContext,
		parentAgent,
		childID,
		content,
		options,
	)
}

// Interrupt signals one authorized resident child without waiting for idle.
func (owner *Service) Interrupt(
	targetID session.SessionID,
	authority subagent.InterruptAuthority,
) error {
	owner.mutex.RLock()
	activeManager := owner.active
	owner.mutex.RUnlock()
	if activeManager == nil {
		return nil
	}
	return activeManager.Interrupt(targetID, authority)
}

// ReportFrom delivers selected child content to its live direct parent.
func (owner *Service) ReportFrom(
	requestContext context.Context,
	childAgent agent.Agent,
	content []llm.ContentBlock,
	options subagent.ReportOptions,
) (llm.MessageID, error) {
	activeManager, err := owner.requireManager()
	if err != nil {
		return "", err
	}
	return activeManager.ReportFrom(
		requestContext,
		childAgent,
		content,
		options,
	)
}

// DrainContinuableChildren releases selected resident direct-child forests.
func (owner *Service) DrainContinuableChildren(
	requestContext context.Context,
	parentAgent agent.Agent,
	childIDs []session.SessionID,
) error {
	activeManager, err := owner.requireManager()
	if err != nil {
		return err
	}
	return activeManager.DrainChildren(requestContext, parentAgent, childIDs)
}

// DrainContinuableDescendants releases descendant forests below exact roots.
func (owner *Service) DrainContinuableDescendants(
	requestContext context.Context,
	parents []agent.Agent,
) error {
	activeManager, err := owner.requireManager()
	if err != nil {
		return err
	}
	return activeManager.DrainDescendants(requestContext, parents)
}

var _ subagent.ContinuableService = (*Service)(nil)
