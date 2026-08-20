package agentloop

import (
	"context"
	"errors"
	"fmt"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/systemprompt"
)

type preparedStep struct {
	rejected bool
	messages []llm.UserMessage
	assembly systemprompt.PromptAssembly
}

func (driver *agentDriver) prepareStep(
	requestContext context.Context,
	target agent.InboxTarget,
	turn int64,
	step int64,
) (preparedStep, error) {
	if err := contextFailure(requestContext); err != nil {
		return preparedStep{}, err
	}
	claimedMessages, err := driver.pending.Claim(target, turn)
	if err != nil {
		return preparedStep{}, err
	}
	assembled, err := driver.prompts.Assemble(
		requestContext,
		systemprompt.AssembleContext{
			Session: driver.subject.conversation,
		},
	)
	if err != nil {
		return preparedStep{}, err
	}
	sections, err := systemprompt.RenderContextSections(assembled)
	if err != nil {
		return preparedStep{}, err
	}
	projected, present, err := driver.projection.project(
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
			Subject:  driver.subject,
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

func (driver *agentDriver) runTurn(
	requestContext context.Context,
) (bool, error) {
	if err := contextFailure(requestContext); err != nil {
		return false, err
	}
	driver.mutex.Lock()
	turn := driver.activity.turn + 1
	driver.mutex.Unlock()
	if turn <= 0 || turn > maxSafeInteger {
		problem := fmt.Errorf(
			"agentloop: Agent %q turn exceeds the safe integer range",
			driver.subject.identifier,
		)
		driver.reportError(requestContext, problem)
		return false, problem
	}
	if _, err := session.AppendSerialized(
		driver.subject.conversation,
		session.TurnStarted,
		session.TurnStart{
			Turn: turn,
		},
	); err != nil {
		driver.reportError(requestContext, err)
		return false, err
	}
	driver.mutex.Lock()
	driver.activity.turn = turn
	driver.mutex.Unlock()

	var ending session.TurnEndReason
	var operationErr error
	target := agent.NextTurn
	for {
		if err := contextFailure(requestContext); err != nil {
			operationErr = err
			ending = session.TurnAborted{
				Reason: driver.durableCancelCause(),
			}
			break
		}
		driver.mutex.Lock()
		step := driver.activity.step + 1
		priorStep := driver.activity.step
		driver.mutex.Unlock()
		prepared, err := driver.prepareStep(
			requestContext,
			target,
			turn,
			step,
		)
		if err != nil {
			operationErr = err
			if requestContext.Err() != nil {
				ending = session.TurnAborted{
					Reason: driver.durableCancelCause(),
				}
			} else {
				ending = session.TurnError{
					Error: failureFromError(err),
				}
				driver.reportError(requestContext, err)
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
			driver.subject.conversation,
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
			driver.reportError(requestContext, err)
			break
		}
		driver.mutex.Lock()
		driver.activity.step = step
		driver.mutex.Unlock()
		stepErr := driver.appendStepMessages(prepared.messages)
		var stepEnding session.TurnEndReason
		if stepErr == nil {
			stepEnding, stepErr = driver.executeStep(
				requestContext,
				turn,
				step,
				prepared,
			)
		}
		_, endErr := session.AppendSerialized(
			driver.subject.conversation,
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
					Reason: driver.durableCancelCause(),
				}
			} else {
				ending = session.TurnError{
					Error: failureFromError(operationErr),
				}
				driver.reportError(requestContext, operationErr)
			}
			break
		}
		if stepEnding != nil &&
			(ending == nil || ending.TurnEndKind() !=
				(session.TurnMaxTokens{}).TurnEndKind()) {
			ending = stepEnding
		}
		if ending != nil && len(driver.pending.NextStep()) == 0 {
			if err := plugin.Publish(
				requestContext,
				driver.subject,
				agent.TurnStopping{
					Subject: driver.subject,
					Turn:    turn,
				},
			); err != nil {
				operationErr = err
				ending = session.TurnError{
					Error: failureFromError(err),
				}
				driver.reportError(requestContext, err)
				break
			}
			if err := contextFailure(requestContext); err != nil {
				operationErr = err
				ending = session.TurnAborted{
					Reason: driver.durableCancelCause(),
				}
				break
			}
		}
		if ending != nil && len(driver.pending.NextStep()) == 0 {
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
		driver.subject.conversation,
		session.TurnEnded,
		session.TurnEnd{
			Turn:   turn,
			Reason: ending,
		},
	); err != nil {
		operationErr = errors.Join(operationErr, err)
		driver.reportError(requestContext, err)
	}
	// A committed turn/end remains this Turn's durability boundary even when
	// the active Turn Context was canceled.
	durabilityContext := context.WithoutCancel(requestContext)
	if err := driver.sessions.Flush(
		durabilityContext,
		driver.subject.conversation,
	); err != nil {
		operationErr = errors.Join(operationErr, err)
		driver.reportError(requestContext, fmt.Errorf(
			"agentloop: flush Session %q after turn %d: %w",
			driver.subject.identifier,
			turn,
			err,
		))
	}
	if operationErr != nil {
		return false, operationErr
	}
	if !driver.pending.HasPending() {
		return false, nil
	}
	return driver.renewTurnContext(requestContext)
}

func (driver *agentDriver) appendStepMessages(
	messages []llm.UserMessage,
) error {
	for _, message := range messages {
		if _, err := session.AppendSurfaceSerialized(
			driver.subject.conversation,
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
