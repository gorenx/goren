package agentloop

import (
	"context"
	"errors"
	"fmt"

	"github.com/gorenx/goren/agent"
	stepstate "github.com/gorenx/goren/agentloop/internal/step"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/systemprompt"
)

// stepProposal is the pre-step decision and detached input for one possible
// durable Step. A rejected proposal never creates step/start.
type stepProposal struct {
	position session.StepPosition
	rejected bool
	messages []agentmessage.UserMessage
	assembly systemprompt.PromptAssembly
}

func (proposal *stepProposal) hasMessages() bool {
	return proposal != nil && len(proposal.messages) != 0
}

// prepareStep claims the target Inbox entries, assembles Prompt Context, and
// resolves agent/pre-step without opening a durable Step.
func (subject *ReactLoopAgent) prepareStep(
	requestContext context.Context,
	target agent.InboxTarget,
	turnNumber int64,
	stepNumber int64,
) (*stepProposal, error) {
	if err := contextFailure(requestContext); err != nil {
		return nil, err
	}
	claimedMessages, err := subject.pending.Claim(target, turnNumber)
	if err != nil {
		return nil, err
	}
	assembled, err := subject.prompts.Assemble(
		requestContext,
		systemprompt.AssembleContext{
			Session: subject.conversation,
		},
	)
	if err != nil {
		return nil, err
	}
	sections, err := systemprompt.RenderContextSections(assembled)
	if err != nil {
		return nil, err
	}
	contextMessage, present, err := subject.visibleContext.Message(
		systemprompt.JoinContextSections(sections),
		sections,
	)
	if err != nil {
		return nil, err
	}
	candidates := claimedMessages
	if present {
		candidates = append(candidates, contextMessage)
	}
	decision, err := agent.ResolvePreStep(
		requestContext,
		agent.PreStepNotice{
			Subject:  subject,
			Messages: candidates,
			Turn:     turnNumber,
			Step:     stepNumber,
		},
		preStepEnterAction{
			messages: candidates,
		},
	)
	if err != nil {
		return nil, err
	}
	if err = contextFailure(requestContext); err != nil {
		return nil, err
	}
	position := session.StepPosition{
		Turn: turnNumber,
		Step: stepNumber,
	}
	switch decision.Kind {
	case agent.PreStepReject:
		return &stepProposal{
			position: position,
			rejected: true,
		}, nil
	case agent.PreStepEnter:
		detached, cloneErr := cloneUserMessages(decision.Messages)
		if cloneErr != nil {
			return nil, cloneErr
		}
		return &stepProposal{
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

// openStep commits step/start and returns the state object that owns the opened
// Step boundary.
func (subject *ReactLoopAgent) openStep(
	requestContext context.Context,
	proposal *stepProposal,
) (*stepstate.Step, error) {
	if proposal == nil || proposal.rejected {
		return nil, errors.New("agentloop: executable Step proposal is required")
	}
	currentStep, err := stepstate.New(
		proposal.position.Turn,
		proposal.position.Step,
	)
	if err != nil {
		return nil, err
	}
	started, err := session.NewEventDraft(
		session.StepStarted,
		proposal.position,
	)
	if err == nil {
		_, err = subject.conversation.Commit(
			requestContext,
			session.Batch(started),
		)
	}
	if err != nil {
		return nil, err
	}
	if err = currentStep.EnterOpen(); err != nil {
		return nil, err
	}
	return currentStep, nil
}

// executeOpenStep runs one already-open Step and always attempts step/end using
// the non-cancelable settlement Context.
func (subject *ReactLoopAgent) executeOpenStep(
	requestContext context.Context,
	currentStep *stepstate.Step,
	proposal *stepProposal,
) (stepstate.StepResult, error) {
	if currentStep == nil || proposal == nil {
		return stepstate.StepResultError, errors.New(
			"agentloop: open Step and proposal are required",
		)
	}
	operationErr := subject.appendStepMessages(
		requestContext,
		proposal.messages,
	)
	result := stepstate.StepResultNone
	if operationErr == nil {
		operationErr = currentStep.EnterRequesting()
	}
	if operationErr == nil {
		result, operationErr = subject.executeModelRequest(
			requestContext,
			currentStep,
			proposal,
		)
	}
	if operationErr != nil {
		if requestContext.Err() != nil {
			result = stepstate.StepResultAborted
		} else {
			result = stepstate.StepResultError
		}
	}
	if result == stepstate.StepResultNone {
		result = stepstate.StepResultError
		operationErr = errors.Join(
			operationErr,
			errors.New("agentloop: Step ended without a result"),
		)
	}
	if err := currentStep.EnterSettling(result); err != nil {
		operationErr = errors.Join(operationErr, err)
	}
	ended, endErr := session.NewEventDraft(
		session.StepEnded,
		proposal.position,
	)
	if endErr == nil {
		_, endErr = subject.conversation.Commit(
			context.WithoutCancel(requestContext),
			session.Batch(ended),
		)
	}
	operationErr = errors.Join(operationErr, endErr)
	if endErr == nil {
		operationErr = errors.Join(operationErr, currentStep.EnterClosed())
	}
	return result, operationErr
}

func (subject *ReactLoopAgent) appendStepMessages(
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
		if _, err = subject.conversation.Commit(
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

func cloneUserMessages(
	entries []agentmessage.UserMessage,
) ([]agentmessage.UserMessage, error) {
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
