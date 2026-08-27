package agentloop

import (
	"context"
	"errors"
	"fmt"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/systemprompt"
)

// stepPlan is one proposed durable Step. A rejected plan is never started; an
// accepted plan produces one StepStarted/StepEnded event pair.
type stepPlan struct {
	position session.StepPosition
	rejected bool
	messages []agentmessage.UserMessage
	assembly systemprompt.PromptAssembly
}

func (current *stepPlan) hasMessages() bool {
	return current != nil && len(current.messages) != 0
}

// stepExecutor owns Step preparation and the complete durable Step execution
// boundary. Turn progression and Turn termination remain with turnRunner.
type stepExecutor struct {
	subject    *ReactLoopAgent
	activity   *activityCoordinator
	pending    *agent.Inbox
	projection *runtimeContextProjection
	prompts    systemprompt.Assembler
	models     llm.LlmRuntime
	toolCalls  *toolCallExecutor

	requestHeaderLogged bool
}

func newStepExecutor(
	subject *ReactLoopAgent,
	activity *activityCoordinator,
	pending *agent.Inbox,
	projection *runtimeContextProjection,
	prompts systemprompt.Assembler,
	models llm.LlmRuntime,
	toolCalls *toolCallExecutor,
) (*stepExecutor, error) {
	if subject == nil || activity == nil || pending == nil || projection == nil ||
		prompts == nil || models == nil || toolCalls == nil {
		return nil, errors.New("agentloop: Step executor dependencies are incomplete")
	}
	return &stepExecutor{
		subject:    subject,
		activity:   activity,
		pending:    pending,
		projection: projection,
		prompts:    prompts,
		models:     models,
		toolCalls:  toolCalls,
	}, nil
}

func (executor *stepExecutor) deactivate() {
	if executor.toolCalls != nil {
		executor.toolCalls.deactivate()
	}
	executor.toolCalls = nil
	executor.models = nil
	executor.prompts = nil
}

func (executor *stepExecutor) prepare(
	requestContext context.Context,
	target agent.InboxTarget,
	turn int64,
	number int64,
) (*stepPlan, error) {
	if err := contextFailure(requestContext); err != nil {
		return nil, err
	}
	claimedMessages, err := executor.pending.Claim(target, turn)
	if err != nil {
		return nil, err
	}
	assembled, err := executor.prompts.Assemble(
		requestContext,
		systemprompt.AssembleContext{
			Session: executor.subject.conversation,
		},
	)
	if err != nil {
		return nil, err
	}
	sections, err := systemprompt.RenderContextSections(assembled)
	if err != nil {
		return nil, err
	}
	projected, present, err := executor.projection.project(
		systemprompt.JoinContextSections(sections),
		sections,
	)
	if err != nil {
		return nil, err
	}
	candidates := claimedMessages
	if present {
		candidates = append(candidates, projected)
	}
	decision, err := agent.ResolvePreStep(
		requestContext,
		agent.PreStepNotice{
			Subject:  executor.subject,
			Messages: candidates,
			Turn:     turn,
			Step:     number,
		},
		preStepEnterAction{
			messages: candidates,
		},
	)
	if err != nil {
		return nil, err
	}
	if err := contextFailure(requestContext); err != nil {
		return nil, err
	}
	position := session.StepPosition{
		Turn: turn,
		Step: number,
	}
	switch decision.Kind {
	case agent.PreStepReject:
		return &stepPlan{
			position: position,
			rejected: true,
		}, nil
	case agent.PreStepEnter:
		detached, err := cloneUserMessages(decision.Messages)
		if err != nil {
			return nil, err
		}
		return &stepPlan{
			position: position,
			messages: detached,
			assembly: assembled,
		}, nil
	default:
		return nil, fmt.Errorf(
			"agentloop: unsupported pre-step decision %q",
			decision.Kind,
		)
	}
}

func (executor *stepExecutor) execute(
	requestContext context.Context,
	current *stepPlan,
) (session.TurnEndReason, error) {
	if current == nil {
		return nil, errors.New("agentloop: Step is nil")
	}
	if current.rejected {
		return nil, errors.New("agentloop: rejected Step cannot execute")
	}
	started, err := session.NewEventDraft(
		session.StepStarted,
		current.position,
	)
	if err != nil {
		return nil, err
	}
	if _, err = executor.subject.conversation.Commit(
		requestContext,
		session.Batch(started),
	); err != nil {
		return nil, err
	}
	executor.activity.acceptStep(current.position.Step)

	operationErr := executor.appendMessages(requestContext, current.messages)
	var ending session.TurnEndReason
	if operationErr == nil {
		ending, operationErr = executor.executeModel(requestContext, current)
	}
	ended, endErr := session.NewEventDraft(
		session.StepEnded,
		current.position,
	)
	if endErr == nil {
		_, endErr = executor.subject.conversation.Commit(
			requestContext,
			session.Batch(ended),
		)
	}
	return ending, errors.Join(operationErr, endErr)
}

func (executor *stepExecutor) appendMessages(
	requestContext context.Context,
	messages []agentmessage.UserMessage,
) error {
	for _, message := range messages {
		draft, err := session.NewSurfaceEventDraft(
			session.UserMessageAdded,
			message,
			session.SurfaceIntent{
				Operation: session.SurfaceAppend(),
			},
		)
		if err != nil {
			return err
		}
		if _, err := executor.subject.conversation.Commit(
			requestContext,
			session.Batch(draft),
		); err != nil {
			return err
		}
	}
	return nil
}

type preStepEnterAction struct {
	messages []agentmessage.UserMessage
}

func (action preStepEnterAction) Execute(
	context.Context,
	agent.PreStepNotice,
) (agent.PreStepDecision, error) {
	return agent.PreStepDecision{
		Kind:     agent.PreStepEnter,
		Messages: action.messages,
	}, nil
}

func cloneUserMessages(entries []agentmessage.UserMessage) ([]agentmessage.UserMessage, error) {
	if entries == nil {
		return nil, nil
	}
	detached := make([]agentmessage.UserMessage, len(entries))
	for index, entry := range entries {
		copyValue, err := agentmessage.CloneUserMessage(entry)
		if err != nil {
			return nil, fmt.Errorf(
				"agentloop: clone user message %d: %w",
				index,
				err,
			)
		}
		detached[index] = copyValue
	}
	return detached, nil
}
