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

// turnRunner owns the durable Turn state machine and Step iteration. Step
// preparation and execution belong to stepExecutor.
type turnRunner struct {
	subject    *ReactLoopAgent
	activity   *activityCoordinator
	pending    *agent.Inbox
	projection *runtimeContextProjection
	events     *agentEventPublisher

	sessions session.LiveStore
	executor *stepExecutor
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
	executor, err := newStepExecutor(
		runner.subject,
		runner.activity,
		runner.pending,
		runner.projection,
		prompts,
		models,
		toolCalls,
	)
	if err != nil {
		return err
	}
	runner.sessions = sessions
	runner.executor = executor
	return requestContext.Err()
}

func (runner *turnRunner) deactivate() {
	if runner.executor != nil {
		runner.executor.deactivate()
	}
	runner.executor = nil
	runner.sessions = nil
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
	turnDraft, err := session.NewEventDraft(
		session.TurnStarted,
		session.TurnStart{
			Turn: turn,
		},
	)
	if err != nil {
		runner.reportError(requestContext, err)
		return false, err
	}
	if _, err := runner.subject.conversation.Commit(requestContext, session.Batch(turnDraft)); err != nil {
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
		stepNumber, priorStep := runner.activity.proposedStep()
		plan, err := runner.executor.prepare(
			requestContext,
			target,
			turn,
			stepNumber,
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
		if plan.rejected {
			ending = session.TurnBlocked{}
			break
		}
		if ending != nil && !plan.hasMessages() {
			break
		}
		if priorStep == 0 && !plan.hasMessages() {
			ending = session.TurnCompleted{}
			break
		}
		stepEnding, stepErr := runner.executor.execute(requestContext, plan)
		if stepErr != nil {
			operationErr = stepErr
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
	turnEndDraft, err := session.NewEventDraft(
		session.TurnEnded,
		session.TurnEnd{
			Turn:   turn,
			Reason: ending,
		},
	)
	if err == nil {
		_, err = runner.subject.conversation.Commit(
			context.WithoutCancel(requestContext),
			session.Batch(turnEndDraft),
		)
	}
	if err != nil {
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

func restoreLastTurn(conversation session.Context) (int64, error) {
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
