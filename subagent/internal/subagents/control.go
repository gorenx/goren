package subagents

import (
	"context"
	"errors"
	"fmt"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/subagent/internal/agentclient"
	sharedexecution "github.com/gorenx/goren/subagent/internal/execution"
)

type agentDirectory interface {
	Find(context.Context, session.SessionID) (agentclient.Agent, error)
}

type childExecutionControl struct {
	agents      agentDirectory
	executions  *sharedexecution.Registry
	modes       map[subagent.Mode]implementation
	continuable continuable
	bound       bound
}

func newChildExecutionControl(agents agentDirectory, executions *sharedexecution.Registry, implementations map[subagent.Mode]implementation) *childExecutionControl {
	control := &childExecutionControl{agents: agents, executions: executions, modes: implementations}
	control.continuable, _ = implementations[subagent.ModeContinuable].(continuable)
	control.bound, _ = implementations[subagent.ModeBound].(bound)
	return control
}

func (control *childExecutionControl) Send(ctx context.Context, parentID, childID session.SessionID, content []agentmessage.ContentBlock, options subagent.FollowupOptions) (agentmessage.MessageID, error) {
	if ctx == nil {
		return "", errors.New("subagent: Send context is nil")
	}
	if err := context.Cause(ctx); err != nil {
		return "", err
	}
	message, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{Content: content, Source: options.Source})
	if err != nil {
		return "", err
	}
	entry, found := control.executions.Find(childID)
	if found {
		if err = control.authorizeParent(ctx, entry, parentID); err != nil {
			return "", err
		}
		if entry.Mode == subagent.ModeBound {
			if control.bound == nil {
				return "", errors.New("subagent: Bound implementation is incomplete")
			}
			return control.bound.Followup(ctx, parentID, childID, message)
		}
		child, findErr := control.agents.Find(ctx, childID)
		if findErr != nil {
			return "", findErr
		}
		if err = child.Followup(ctx, message); err != nil {
			return "", err
		}
		return message.StableID(), nil
	}
	if control.bound != nil {
		boundChild, checkErr := control.bound.HasBinding(ctx, parentID, childID)
		if checkErr != nil {
			return "", checkErr
		}
		if boundChild {
			return control.bound.Followup(ctx, parentID, childID, message)
		}
	}
	if control.continuable == nil {
		return "", errors.New("subagent: Continuable implementation does not support resume")
	}
	return control.continuable.Resume(ctx, parentID, childID, message)
}

func (control *childExecutionControl) Interrupt(ctx context.Context, childID session.SessionID, authority subagent.InterruptAuthority) error {
	entry, found := control.executions.Find(childID)
	if !found {
		return nil
	}
	if err := control.authorizeInterrupt(ctx, entry, authority); err != nil {
		return err
	}
	selected := control.modes[entry.Mode]
	if selected == nil {
		return fmt.Errorf("subagent: child mode %q has no control implementation", entry.Mode)
	}
	return selected.Interrupt(ctx, childID)
}

func (control *childExecutionControl) authorizeParent(ctx context.Context, entry sharedexecution.Entry, parentID session.SessionID) error {
	if entry.Parent.AgentID == parentID {
		if _, err := control.agents.Find(ctx, parentID); err == nil {
			return nil
		}
	}
	return &subagent.Error{Code: subagent.ErrorUnauthorized, Message: fmt.Sprintf("subagent %q delivery requires its exact live parent Agent", entry.Subject.AgentID)}
}

func (control *childExecutionControl) authorizeInterrupt(ctx context.Context, entry sharedexecution.Entry, authority subagent.InterruptAuthority) error {
	switch evidence := authority.(type) {
	case subagent.UserInterruptAuthority:
		if entry.Parent.AgentID == evidence.ParentSessionID {
			return nil
		}
	case subagent.AncestorInterruptAuthority:
		if control.isLiveAncestor(ctx, entry.Subject.AgentID, evidence.AgentID) {
			return nil
		}
	}
	return &subagent.Error{Code: subagent.ErrorUnauthorized, Message: fmt.Sprintf("interrupting subagent %q is not authorized", entry.Subject.AgentID)}
}

func (control *childExecutionControl) isLiveAncestor(ctx context.Context, childID, candidateID session.SessionID) bool {
	seen := make(map[session.SessionID]struct{})
	current, err := control.agents.Find(ctx, childID)
	for err == nil {
		view, readErr := current.Read(ctx)
		if readErr != nil || view.Header.ParentSession == nil {
			return false
		}
		parentID := *view.Header.ParentSession
		if parentID == candidateID {
			_, findErr := control.agents.Find(ctx, candidateID)
			return findErr == nil
		}
		if _, duplicate := seen[parentID]; duplicate {
			return false
		}
		seen[parentID] = struct{}{}
		current, err = control.agents.Find(ctx, parentID)
	}
	return false
}
