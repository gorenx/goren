package subagents

import (
	"context"
	"errors"
	"fmt"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	sharedexecution "github.com/gorenx/goren/subagent/internal/execution"
)

// childExecutionControl owns child delivery, interruption authorization, and
// structural Agent-disposal routing. It does not own module admission or any
// mode's config and lifecycle decisions.
type childExecutionControl struct {
	agents     agent.Registry
	executions *sharedexecution.Registry
	// Key is a canonical Subagent mode. Value is its complete implementation
	// for the current open Service cycle.
	modes       map[subagent.Mode]implementation
	continuable continuable
	bound       bound
}

func newChildExecutionControl(
	agentRegistry agent.Registry,
	executionRegistry *sharedexecution.Registry,
	implementations map[subagent.Mode]implementation,
) *childExecutionControl {
	control := &childExecutionControl{
		agents:     agentRegistry,
		executions: executionRegistry,
		modes:      implementations,
	}
	if candidate := implementations[subagent.ModeContinuable]; candidate != nil {
		control.continuable, _ = candidate.(continuable)
	}
	if candidate := implementations[subagent.ModeBound]; candidate != nil {
		control.bound, _ = candidate.(bound)
	}
	return control
}

func (control *childExecutionControl) Send(
	ctx context.Context,
	parentAgent agent.Agent,
	childID session.SessionID,
	content []agentmessage.ContentBlock,
	options subagent.FollowupOptions,
) (agentmessage.MessageID, error) {
	if ctx == nil {
		return "", errors.New("subagent: Send context is nil")
	}
	if err := context.Cause(ctx); err != nil {
		return "", err
	}
	messageValue, err := agentmessage.NewUserMessage(
		agentmessage.UserMessageInput{
			Content: content,
			Source:  options.Source,
		},
	)
	if err != nil {
		return "", err
	}
	entry, found := control.executions.Find(childID)
	if found {
		return control.sendResident(ctx, parentAgent, entry, messageValue)
	}
	return control.sendCold(ctx, parentAgent, childID, messageValue)
}

func (control *childExecutionControl) sendResident(
	ctx context.Context,
	parentAgent agent.Agent,
	entry sharedexecution.Entry,
	messageValue agentmessage.UserMessage,
) (agentmessage.MessageID, error) {
	if err := control.authorizeParent(entry, parentAgent); err != nil {
		return "", err
	}
	if entry.Mode == subagent.ModeBound {
		if control.bound == nil {
			return "", errors.New(
				"subagent: Bound implementation is incomplete",
			)
		}
		return control.bound.Followup(
			ctx,
			parentAgent,
			entry.Subject.ID(),
			messageValue,
		)
	}
	if err := entry.Subject.Followup(messageValue); err != nil {
		return "", err
	}
	return messageValue.StableID(), nil
}

func (control *childExecutionControl) sendCold(
	ctx context.Context,
	parentAgent agent.Agent,
	childID session.SessionID,
	messageValue agentmessage.UserMessage,
) (agentmessage.MessageID, error) {
	if control.bound == nil {
		return control.resumeCold(ctx, parentAgent, childID, messageValue)
	}
	boundChild, err := control.bound.HasBinding(
		ctx,
		parentAgent,
		childID,
	)
	if err != nil {
		return "", err
	}
	if !boundChild {
		return control.resumeCold(ctx, parentAgent, childID, messageValue)
	}
	return control.bound.Followup(
		ctx,
		parentAgent,
		childID,
		messageValue,
	)
}

func (control *childExecutionControl) resumeCold(
	ctx context.Context,
	parentAgent agent.Agent,
	childID session.SessionID,
	messageValue agentmessage.UserMessage,
) (agentmessage.MessageID, error) {
	if control.continuable == nil {
		return "", errors.New(
			"subagent: Continuable implementation does not support resume",
		)
	}
	return control.continuable.Resume(
		ctx,
		parentAgent,
		childID,
		messageValue,
	)
}

func (control *childExecutionControl) Interrupt(
	ctx context.Context,
	childID session.SessionID,
	authority subagent.InterruptAuthority,
) error {
	entry, found := control.executions.Find(childID)
	if !found {
		return nil
	}
	if err := authorizeInterrupt(
		control.agents,
		entry,
		authority,
	); err != nil {
		return err
	}
	selected := control.modes[entry.Mode]
	if selected == nil {
		return fmt.Errorf(
			"subagent: child mode %q has no control implementation",
			entry.Mode,
		)
	}
	return selected.Interrupt(ctx, childID)
}

func (control *childExecutionControl) AgentDisposed(
	ctx context.Context,
	subject agent.Agent,
) error {
	if subject == nil {
		return nil
	}
	entry, found := control.executions.Find(subject.ID())
	if !found || !agent.Same(entry.Subject, subject) {
		return nil
	}
	return entry.Execution.StopAndWait(
		ctx,
		sharedexecution.StopExternal,
	)
}

func (control *childExecutionControl) authorizeParent(
	entry sharedexecution.Entry,
	parentAgent agent.Agent,
) error {
	if parentAgent != nil && control.agents.Contains(parentAgent) &&
		agent.Same(entry.Parent, parentAgent) {
		return nil
	}
	return &subagent.Error{
		Code: subagent.ErrorUnauthorized,
		Message: fmt.Sprintf(
			"subagent %q delivery requires its exact live parent Agent",
			entry.Subject.ID(),
		),
	}
}

func authorizeInterrupt(
	agentRegistry agent.Registry,
	entry sharedexecution.Entry,
	authority subagent.InterruptAuthority,
) error {
	switch evidence := authority.(type) {
	case subagent.UserInterruptAuthority:
		if entry.Parent.ID() == evidence.ParentSessionID {
			return nil
		}
	case subagent.AncestorInterruptAuthority:
		if evidence.Agent != nil && agentRegistry.Contains(evidence.Agent) &&
			isLiveAncestor(agentRegistry, entry.Subject, evidence.Agent) {
			return nil
		}
	}
	return &subagent.Error{
		Code: subagent.ErrorUnauthorized,
		Message: fmt.Sprintf(
			"interrupting subagent %q is not authorized",
			entry.Subject.ID(),
		),
	}
}

func isLiveAncestor(
	agentRegistry agent.Registry,
	childAgent agent.Agent,
	candidate agent.Agent,
) bool {
	seen := make(map[session.SessionID]struct{})
	parentID := childAgent.SessionValue().Header().ParentSession
	for parentID != nil {
		if _, duplicate := seen[*parentID]; duplicate {
			return false
		}
		seen[*parentID] = struct{}{}
		ancestor, found := agentRegistry.Get(*parentID)
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
