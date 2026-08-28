package bound

import (
	"context"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
)

// HasBinding reports only an exact user Session-owned durable Binding.
func (owner *Service) HasBinding(
	requestContext context.Context,
	parentAgent agent.Agent,
	childID session.SessionID,
) (bool, error) {
	if err := checkContext(requestContext, "Bound HasBinding"); err != nil {
		return false, err
	}
	if err := owner.authorizeParent(parentAgent); err != nil {
		return false, err
	}
	view, err := readBoundProjection(
		owner.dependencies.Projections,
		parentAgent.SessionValue(),
	)
	if err != nil {
		return false, err
	}
	_, found := view.Binding(childID)
	return found, nil
}

// Send serializes materialization and explicit message admission for one
// Binding while leaving other Bound children independent.
func (owner *Service) Send(
	requestContext context.Context,
	parentAgent agent.Agent,
	childID session.SessionID,
	messageValue agentmessage.UserMessage,
) (agentmessage.MessageID, error) {
	if err := checkContext(requestContext, "Bound Send"); err != nil {
		return "", err
	}
	if err := owner.authorizeParent(parentAgent); err != nil {
		return "", err
	}
	view, err := readBoundProjection(
		owner.dependencies.Projections,
		parentAgent.SessionValue(),
	)
	if err != nil {
		return "", err
	}
	bindingValue, found := view.Binding(childID)
	if !found {
		return "", bindingNotFound(childID)
	}
	worker, err := owner.workers.acquire(parentAgent, bindingValue)
	if err != nil {
		return "", err
	}
	return worker.send(requestContext, messageValue)
}

// Interrupt cancels the current turn but retains queued Bound work and the
// resident Agent epoch.
func (owner *Service) Interrupt(
	requestContext context.Context,
	childID session.SessionID,
) error {
	if err := checkContext(requestContext, "Bound Interrupt"); err != nil {
		return err
	}
	return owner.workers.interrupt(requestContext, childID)
}
