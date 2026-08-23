package agentloop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/tools"
)

// turnRunner owns the durable Turn/Step state machine. Activity admission,
// model request execution, and Tool scheduling belong to its collaborators.
type turnRunner struct {
	subject    *ReactLoopAgent
	activity   *activityCoordinator
	pending    *agent.Inbox
	projection *runtimeContextProjection
	events     *agentEventPublisher

	sessions session.LiveStore
	prompts  systemprompt.Assembler
	requests *modelRequester
}

func newTurnRunner(
	subject *ReactLoopAgent,
	activity *activityCoordinator,
	pending *agent.Inbox,
	projection *runtimeContextProjection,
	events *agentEventPublisher,
) *turnRunner {
	return &turnRunner{
		subject:    subject,
		activity:   activity,
		pending:    pending,
		projection: projection,
		events:     events,
	}
}

func (runner *turnRunner) activate(
	requestContext context.Context,
	sessions session.LiveStore,
	models llm.LlmRuntime,
	toolRuntime tools.ToolRuntime,
	prompts systemprompt.Assembler,
	maxParallelToolCalls int,
) error {
	if requestContext == nil {
		return errors.New("agentloop: Turn runner activation Context is nil")
	}
	if err := requestContext.Err(); err != nil {
		return err
	}
	if sessions == nil || models == nil || toolRuntime == nil || prompts == nil {
		return errors.New("agentloop: Turn runner dependencies are incomplete")
	}
	toolCalls, err := newToolCallExecutor(
		runner.subject,
		runner.pending,
		toolRuntime,
		maxParallelToolCalls,
	)
	if err != nil {
		return err
	}
	runner.sessions = sessions
	runner.prompts = prompts
	runner.requests = newModelRequester(
		runner.subject,
		models,
		toolCalls,
	)
	return requestContext.Err()
}

func (runner *turnRunner) deactivate() {
	if runner.requests != nil {
		runner.requests.deactivate()
	}
	runner.requests = nil
	runner.sessions = nil
	runner.prompts = nil
}

func (runner *turnRunner) reportError(
	requestContext context.Context,
	problem error,
) {
	runner.events.publishError(
		requestContext,
		runner.activity.snapshotPosition(),
		problem,
	)
}

type preparedStep struct {
	rejected bool
	messages []llm.UserMessage
	assembly systemprompt.PromptAssembly
}

func (runner *turnRunner) prepareStep(
	requestContext context.Context,
	target agent.InboxTarget,
	turn int64,
	step int64,
) (preparedStep, error) {
	if err := contextFailure(requestContext); err != nil {
		return preparedStep{}, err
	}
	claimedMessages, err := runner.pending.Claim(target, turn)
	if err != nil {
		return preparedStep{}, err
	}
	assembled, err := runner.prompts.Assemble(
		requestContext,
		systemprompt.AssembleContext{
			Session: runner.subject.conversation,
		},
	)
	if err != nil {
		return preparedStep{}, err
	}
	sections, err := systemprompt.RenderContextSections(assembled)
	if err != nil {
		return preparedStep{}, err
	}
	projected, present, err := runner.projection.project(
		systemprompt.JoinContextSections(sections),
		sections,
	)
	if err != nil {
		return preparedStep{}, err
	}
	candidates := claimedMessages
	if present {
		candidates = append(candidates, projected)
	}
	decision, err := agent.ResolvePreStep(
		requestContext,
		agent.PreStepNotice{
			Subject:  runner.subject,
			Messages: candidates,
			Turn:     turn,
			Step:     step,
		},
		agent.PreStepActionFunc(func(
			context.Context,
			agent.PreStepNotice,
		) (agent.PreStepDecision, error) {
			return agent.PreStepDecision{
				Kind:     agent.PreStepEnter,
				Messages: candidates,
			}, nil
		}),
	)
	if err != nil {
		return preparedStep{}, err
	}
	if err := contextFailure(requestContext); err != nil {
		return preparedStep{}, err
	}
	switch decision.Kind {
	case agent.PreStepReject:
		return preparedStep{
			rejected: true,
		}, nil
	case agent.PreStepEnter:
		detached, err := cloneUserMessages(decision.Messages)
		if err != nil {
			return preparedStep{}, err
		}
		return preparedStep{
			messages: detached,
			assembly: assembled,
		}, nil
	default:
		return preparedStep{}, fmt.Errorf(
			"agentloop: unsupported pre-step decision %q",
			decision.Kind,
		)
	}
}

func (runner *turnRunner) runTurn(
	requestContext context.Context,
) (bool, error) {
	if err := contextFailure(requestContext); err != nil {
		return false, err
	}
	turn := runner.activity.proposedTurn()
	if turn <= 0 || turn > maxSafeInteger {
		problem := fmt.Errorf(
			"agentloop: Agent %q turn exceeds the safe integer range",
			runner.subject.identifier,
		)
		runner.reportError(requestContext, problem)
		return false, problem
	}
	if _, err := session.AppendSerialized(
		runner.subject.conversation,
		session.TurnStarted,
		session.TurnStart{
			Turn: turn,
		},
	); err != nil {
		runner.reportError(requestContext, err)
		return false, err
	}
	runner.activity.acceptTurn(turn)

	var ending session.TurnEndReason
	var operationErr error
	target := agent.NextTurn
	for {
		if err := contextFailure(requestContext); err != nil {
			operationErr = err
			ending = session.TurnAborted{
				Reason: runner.activity.durableCancelCause(),
			}
			break
		}
		step, priorStep := runner.activity.proposedStep()
		prepared, err := runner.prepareStep(
			requestContext,
			target,
			turn,
			step,
		)
		if err != nil {
			operationErr = err
			if requestContext.Err() != nil {
				ending = session.TurnAborted{
					Reason: runner.activity.durableCancelCause(),
				}
			} else {
				ending = session.TurnError{
					Error: failureFromError(err),
				}
				runner.reportError(requestContext, err)
			}
			break
		}
		if prepared.rejected {
			ending = session.TurnBlocked{}
			break
		}
		if ending != nil && len(prepared.messages) == 0 {
			break
		}
		if priorStep == 0 && len(prepared.messages) == 0 {
			ending = session.TurnCompleted{}
			break
		}
		if _, err := session.AppendSerialized(
			runner.subject.conversation,
			session.StepStarted,
			session.StepPosition{
				Turn: turn,
				Step: step,
			},
		); err != nil {
			operationErr = err
			ending = session.TurnError{
				Error: failureFromError(err),
			}
			runner.reportError(requestContext, err)
			break
		}
		runner.activity.acceptStep(step)
		stepErr := runner.appendStepMessages(prepared.messages)
		var stepEnding session.TurnEndReason
		if stepErr == nil {
			stepEnding, stepErr = runner.requests.executeStep(
				requestContext,
				turn,
				step,
				prepared,
			)
		}
		_, endErr := session.AppendSerialized(
			runner.subject.conversation,
			session.StepEnded,
			session.StepPosition{
				Turn: turn,
				Step: step,
			},
		)
		if stepErr != nil || endErr != nil {
			operationErr = errors.Join(stepErr, endErr)
			if requestContext.Err() != nil {
				ending = session.TurnAborted{
					Reason: runner.activity.durableCancelCause(),
				}
			} else {
				ending = session.TurnError{
					Error: failureFromError(operationErr),
				}
				runner.reportError(requestContext, operationErr)
			}
			break
		}
		if stepEnding != nil &&
			(ending == nil || ending.TurnEndKind() !=
				(session.TurnMaxTokens{}).TurnEndKind()) {
			ending = stepEnding
		}
		nextStepEmpty := ending != nil && len(runner.pending.NextStep()) == 0
		if nextStepEmpty {
			if err := runner.events.publishTurnStopping(
				requestContext,
				turn,
			); err != nil {
				operationErr = err
				ending = session.TurnError{
					Error: failureFromError(err),
				}
				runner.reportError(requestContext, err)
				break
			}
			if err := contextFailure(requestContext); err != nil {
				operationErr = err
				ending = session.TurnAborted{
					Reason: runner.activity.durableCancelCause(),
				}
				break
			}
		}
		if nextStepEmpty {
			break
		}
		target = agent.NextStep
	}
	if ending == nil {
		ending = session.TurnError{
			Error: llm.LlmFailure{
				Message: "turn ended without a reason",
				Code:    "UNKNOWN",
			},
		}
	}
	if _, err := session.AppendSerialized(
		runner.subject.conversation,
		session.TurnEnded,
		session.TurnEnd{
			Turn:   turn,
			Reason: ending,
		},
	); err != nil {
		operationErr = errors.Join(operationErr, err)
		runner.reportError(requestContext, err)
	}
	// A committed turn/end remains this Turn's durability boundary even when
	// the active Turn Context was canceled.
	durabilityContext := context.WithoutCancel(requestContext)
	if err := runner.sessions.Flush(
		durabilityContext,
		runner.subject.conversation,
	); err != nil {
		operationErr = errors.Join(operationErr, err)
		runner.reportError(requestContext, fmt.Errorf(
			"agentloop: flush Session %q after turn %d: %w",
			runner.subject.identifier,
			turn,
			err,
		))
	}
	if operationErr != nil {
		return false, operationErr
	}
	if !runner.pending.HasPending() {
		return false, nil
	}
	return runner.activity.renewTurnContext(requestContext)
}

func (runner *turnRunner) appendStepMessages(
	messages []llm.UserMessage,
) error {
	for _, message := range messages {
		if _, err := session.AppendSurfaceSerialized(
			runner.subject.conversation,
			session.UserMessageAdded,
			message,
			session.SurfaceIntent{
				Operation: session.SurfaceAppend(),
			},
		); err != nil {
			return err
		}
	}
	return nil
}

func failureFromError(problem error) llm.LlmFailure {
	var llmProblem *llm.LlmError
	if errors.As(problem, &llmProblem) {
		return llmProblem.Failure()
	}
	if problem == nil {
		return llm.LlmFailure{
			Message: "unknown Agent Loop failure",
			Code:    "UNKNOWN",
		}
	}
	return llm.LlmFailure{
		Message: problem.Error(),
		Code:    "UNKNOWN",
	}
}

func cloneUserMessages(entries []llm.UserMessage) ([]llm.UserMessage, error) {
	if entries == nil {
		return nil, nil
	}
	detached := make([]llm.UserMessage, len(entries))
	for index, entry := range entries {
		copyValue, err := llm.CloneUserMessage(entry)
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

func restoreLastTurn(conversation *session.Session) (int64, error) {
	entries := conversation.Events()
	for index := len(entries) - 1; index >= 0; index-- {
		if entries[index].Type != session.TurnStartEventName {
			continue
		}
		var started session.TurnStart
		if err := json.Unmarshal(entries[index].Data, &started); err != nil ||
			started.Turn <= 0 || started.Turn > maxSafeInteger {
			return 0, fmt.Errorf(
				"agentloop: invalid persisted turn/start at seq %d",
				entries[index].Seq,
			)
		}
		return started.Turn, nil
	}
	return 0, nil
}
