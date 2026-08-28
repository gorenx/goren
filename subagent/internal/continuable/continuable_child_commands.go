package continuable

import (
	"context"
	"errors"
	"fmt"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/subagent"
	sharedexecution "github.com/gorenx/goren/subagent/internal/execution"
)

func (child *continuableChild) handleStart(
	requestContext context.Context,
	input startInput,
) (subagent.Execution, error) {
	workContext, cancelWork := child.operationContext(requestContext)
	defer cancelWork()
	if err := checkContext(workContext, "Continuable Start"); err != nil {
		return nil, err
	}
	if err := requireLiveParent(child.agents, input.parent); err != nil {
		return nil, err
	}
	if child.current != nil {
		return nil, duplicateChild(child.id)
	}
	if err := child.materializer.assertAvailable(child.id); err != nil {
		return nil, err
	}
	if input.identityRequested {
		if err := child.materializer.assertPersistedAvailable(
			workContext,
			child.id,
		); err != nil {
			return nil, err
		}
	}
	handle, err := child.materializer.create(
		workContext,
		child.id,
		input.descriptor,
		input.request,
		input.lineage,
		input.seed,
		input.seedLength,
	)
	if err != nil {
		return nil, err
	}
	prompt, err := agentmessage.NewUserMessage(
		agentmessage.UserMessageInput{
			Content: input.request.Prompt,
			Source:  agentmessage.UserMessageSource{},
		},
	)
	if err != nil {
		return nil, errors.Join(
			err,
			handle.Dispose(context.WithoutCancel(workContext)),
		)
	}
	if err = handle.Subject.Followup(prompt); err != nil {
		return nil, errors.Join(
			err,
			handle.Dispose(context.WithoutCancel(workContext)),
		)
	}
	current, err := child.publish(
		handle,
		input.parent,
		input.seedBuilder,
	)
	if err != nil {
		return nil, errors.Join(
			err,
			handle.Dispose(context.WithoutCancel(workContext)),
		)
	}
	current.watch()
	return current.execution, nil
}

func (child *continuableChild) handleResume(
	requestContext context.Context,
	parentAgent agent.Agent,
	messageValue agentmessage.UserMessage,
) (agentmessage.MessageID, error) {
	workContext, cancelWork := child.operationContext(requestContext)
	defer cancelWork()
	if err := checkContext(workContext, "Continuable Resume"); err != nil {
		return "", err
	}
	if err := requireLiveParent(child.agents, parentAgent); err != nil {
		return "", err
	}
	for {
		current := child.current
		if current != nil &&
			current.execution.State() != subagent.ExecutionActive {
			if err := current.execution.Wait(workContext); err != nil {
				return "", err
			}
			if child.current == current {
				child.current = nil
			}
			continue
		}
		if current == nil {
			handle, seedBuilder, err := child.materializer.resume(
				workContext,
				parentAgent,
				child.id,
			)
			if err != nil {
				return "", err
			}
			if err = handle.Subject.Followup(messageValue); err != nil {
				return "", errors.Join(
					err,
					handle.Dispose(context.WithoutCancel(workContext)),
				)
			}
			current, err = child.publish(
				handle,
				parentAgent,
				seedBuilder,
			)
			if err != nil {
				return "", errors.Join(
					err,
					handle.Dispose(context.WithoutCancel(workContext)),
				)
			}
			current.watch()
		} else {
			if current.parent.ID() != parentAgent.ID() ||
				!child.agents.Contains(parentAgent) {
				return "", unauthorized(
					fmt.Sprintf(
						"subagent %q delivery requires its exact live parent",
						child.id,
					),
				)
			}
			if err := current.handle.Subject.Followup(messageValue); err != nil {
				return "", err
			}
			current.notify()
		}
		return messageValue.StableID(), nil
	}
}

func (child *continuableChild) handleInterrupt() {
	current := child.current
	if current == nil ||
		current.execution.State() != subagent.ExecutionActive {
		return
	}
	current.handle.Subject.Cancel(
		agent.ParentCancel{},
		agent.CancelOptions{
			KeepInbox: true,
		},
	)
}

func (child *continuableChild) publish(
	handle agent.Handle,
	parentAgent agent.Agent,
	seedBuilder string,
) (*residentExecution, error) {
	runID, err := sharedexecution.NewRunID()
	if err != nil {
		return nil, err
	}
	resident := &residentExecution{
		owner:       child,
		agents:      child.registry.dependencies.Agents,
		descendants: child.registry.dependencies.Descendants,
		sessions:    child.registry.dependencies.Sessions,
		publisher:   child.registry.dependencies.Publisher,
		failures:    child.registry.dependencies.Failures,
		executions:  child.registry.dependencies.Executions,
		handle:      handle,
		parent:      parentAgent,
		seedBuilder: seedBuilder,
		boundary:    handle.Subject.SessionValue().Seq(),
		wake:        make(chan struct{}, 1),
	}
	running, err := sharedexecution.New(
		runID,
		handle.Subject.ID(),
		resident,
	)
	if err != nil {
		return nil, err
	}
	resident.execution = running
	child.current = resident
	if err = sharedexecution.Publish(
		resident.executions,
		resident.publisher,
		sharedexecution.Entry{
			Execution: running,
			Mode:      subagent.ModeContinuable,
			Parent:    parentAgent,
			Subject:   handle.Subject,
			Closing:   handle.ClosingSignal(),
		},
		seedBuilder,
	); err != nil {
		if child.current == resident {
			child.current = nil
		}
		return nil, err
	}
	return resident, nil
}
