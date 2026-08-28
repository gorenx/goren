package bound

import (
	"context"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

// Start routes one already-committed binding to its child.
func (owner *Service) Start(
	ctx context.Context,
	command subagent.BoundStartCommand,
) (subagent.Execution, error) {
	if err := checkContext(ctx, "Bound Start"); err != nil {
		return nil, err
	}
	parentAgent := command.Parent()
	if err := owner.authorizeParent(parentAgent); err != nil {
		return nil, err
	}
	child, err := owner.children.acquire(parentAgent, command.ChildID())
	if err != nil {
		return nil, err
	}
	return child.start(ctx)
}

// StartBindings attempts every committed binding without returning a child
// failure to the Agent Session-start publication path. Different Bound
// children may materialize concurrently.
func (owner *Service) StartBindings(
	ctx context.Context,
	parentAgent agent.Agent,
) error {
	if err := checkContext(ctx, "Bound StartBindings"); err != nil {
		return err
	}
	if parentAgent == nil || parentAgent.SessionValue() == nil {
		return nil
	}
	view, err := readBoundProjection(
		owner.dependencies.Projections,
		parentAgent.SessionValue(),
	)
	if err != nil {
		return err
	}
	if len(view.Bindings) == 0 {
		return nil
	}
	if owner.dependencies.Agents == nil ||
		!owner.dependencies.Agents.Contains(parentAgent) {
		return nil
	}
	var starts sync.WaitGroup
	for _, binding := range view.Bindings {
		starts.Add(1)
		go func(childID session.SessionID) {
			defer starts.Done()
			owner.startBinding(ctx, parentAgent, childID)
		}(binding.ChildSessionID)
	}
	starts.Wait()
	return nil
}

func (owner *Service) startBinding(
	ctx context.Context,
	parentAgent agent.Agent,
	childID session.SessionID,
) {
	command, err := subagent.NewBoundStart(parentAgent, childID)
	if err != nil {
		owner.reportMaterializationFailure(parentAgent.ID(), childID, err)
		return
	}
	_, _ = owner.Start(ctx, command)
}

// Send serializes materialization and message admission for one Bound child
// while leaving other Bound children independent.
func (owner *Service) Send(
	ctx context.Context,
	parentAgent agent.Agent,
	childID session.SessionID,
	messageValue agentmessage.UserMessage,
) (agentmessage.MessageID, error) {
	if err := checkContext(ctx, "Bound Send"); err != nil {
		return "", err
	}
	if err := owner.authorizeParent(parentAgent); err != nil {
		return "", err
	}
	child, err := owner.children.acquire(parentAgent, childID)
	if err != nil {
		return "", err
	}
	return child.send(ctx, messageValue)
}

// Interrupt cancels the current turn but retains queued Bound work and the
// resident Agent epoch.
func (owner *Service) Interrupt(
	ctx context.Context,
	childID session.SessionID,
) error {
	if err := checkContext(ctx, "Bound Interrupt"); err != nil {
		return err
	}
	return owner.children.interrupt(ctx, childID)
}

// Close stops every Bound epoch owned by this Service.
func (owner *Service) Close(ctx context.Context) error {
	return owner.children.close(ctx)
}
