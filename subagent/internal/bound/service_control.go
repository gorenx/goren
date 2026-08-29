package bound

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	boundcontract "github.com/gorenx/goren/subagent/bound"
)

// Deliver resolves one Bound-owned address and durably admits a source input.
func (owner *Service) Deliver(
	requestContext context.Context,
	addressValue boundcontract.Address,
	inputValue boundcontract.Input,
) (boundcontract.Receipt, error) {
	if err := checkContext(requestContext, "Bound Deliver"); err != nil {
		return boundcontract.Receipt{}, err
	}
	if addressValue.SessionID == "" || addressValue.Name == "" {
		return boundcontract.Receipt{}, errors.New(
			"subagent: Bound address is incomplete",
		)
	}
	detached, err := boundcontract.SnapshotInput(inputValue)
	if err != nil {
		return boundcontract.Receipt{}, err
	}
	parentAgent, found := owner.dependencies.Agents.Get(
		addressValue.SessionID,
	)
	if !found {
		return boundcontract.Receipt{}, &subagent.Error{
			Code:    subagent.ErrorUnauthorized,
			Message: "Bound input requires an exact live parent Agent",
		}
	}
	if err = owner.authorizeParent(parentAgent); err != nil {
		return boundcontract.Receipt{}, err
	}
	view, err := readBoundProjection(
		owner.dependencies.Projections,
		parentAgent.SessionValue(),
	)
	if err != nil {
		return boundcontract.Receipt{}, err
	}
	bindingValue, found := view.BindingNamed(addressValue.Name)
	if !found {
		return boundcontract.Receipt{}, namedBindingNotFound(addressValue.Name)
	}
	worker, err := owner.workers.acquire(parentAgent, bindingValue)
	if err != nil {
		return boundcontract.Receipt{}, err
	}
	return worker.deliver(requestContext, detached)
}

var _ boundcontract.Inbox = (*Service)(nil)

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

// Followup serializes materialization and explicit parent followup admission
// for one Binding while leaving other Bound children independent.
func (owner *Service) Followup(
	requestContext context.Context,
	parentAgent agent.Agent,
	childID session.SessionID,
	messageValue agentmessage.UserMessage,
) (agentmessage.MessageID, error) {
	if err := checkContext(requestContext, "Bound Followup"); err != nil {
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
	return worker.followup(requestContext, messageValue)
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
