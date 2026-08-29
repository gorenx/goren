package agentloop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentloop/internal/execution"
	stepstate "github.com/gorenx/goren/agentloop/internal/step"
	turnstate "github.com/gorenx/goren/agentloop/internal/turn"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

// runTurn owns the durable Turn boundary while the turn package owns only the
// legal in-memory state transitions. It returns true when durable pending input
// requires a successor Turn.
func (subject *ReactLoopAgent) runTurn(
	requestContext context.Context,
) (bool, error) {
	if err := contextFailure(requestContext); err != nil {
		return false, err
	}
	turnNumber := subject.lastTurn + 1
	if turnNumber <= 0 || turnNumber > maxSafeInteger {
		problem := fmt.Errorf(
			"agentloop: Agent %q Turn exceeds the safe integer range",
			subject.identifier,
		)
		subject.publishError(requestContext, subject.lastTurn, 0, problem)
		return false, problem
	}
	currentTurn, err := turnstate.New(turnNumber)
	if err != nil {
		return false, err
	}
	started, err := session.NewEventDraft(
		session.TurnStarted,
		session.TurnStart{
			Turn: turnNumber,
		},
	)
	if err == nil {
		_, err = subject.conversation.Commit(
			requestContext,
			session.Batch(started),
		)
	}
	if err != nil {
		subject.publishError(requestContext, turnNumber, 0, err)
		return false, err
	}
	if err = currentTurn.EnterOpen(); err != nil {
		return false, err
	}
	subject.lastTurn = turnNumber

	var operationErr error
	target := agent.NextTurn
	for {
		if err = contextFailure(requestContext); err != nil {
			operationErr = err
			break
		}
		stepNumber, proposalErr := currentTurn.ProposedStep()
		if proposalErr != nil {
			operationErr = proposalErr
			break
		}
		proposal, proposalErr := subject.prepareStep(
			requestContext,
			target,
			turnNumber,
			stepNumber,
		)
		if proposalErr != nil {
			operationErr = proposalErr
			break
		}
		if proposal.rejected {
			operationErr = currentTurn.EnterSettling(
				turnstate.TurnResultBlocked,
			)
			break
		}
		if currentTurn.ResultValue() != turnstate.TurnResultNone &&
			currentTurn.ResultValue() != turnstate.TurnResultContinue &&
			!proposal.hasMessages() {
			operationErr = currentTurn.EnterSettling(
				currentTurn.ResultValue(),
			)
			break
		}
		if currentTurn.LastStep() == 0 && !proposal.hasMessages() {
			operationErr = currentTurn.EnterSettling(
				turnstate.TurnResultCompleted,
			)
			break
		}
		currentStep, openErr := subject.openStep(requestContext, proposal)
		if openErr != nil {
			operationErr = openErr
			break
		}
		if openErr = currentTurn.EnterStepping(stepNumber); openErr != nil {
			operationErr = openErr
			break
		}
		stepResult, stepErr := subject.executeOpenStep(
			requestContext,
			currentStep,
			proposal,
		)
		turnResult := turnResultFromStep(stepResult)
		if openErr = currentTurn.EnterOpenAfterStep(turnResult); openErr != nil {
			operationErr = errors.Join(stepErr, openErr)
			break
		}
		if stepErr != nil {
			operationErr = stepErr
			break
		}
		if stepResult != stepstate.StepResultContinue &&
			len(subject.pending.NextStep()) == 0 {
			if err = currentTurn.EnterStopping(); err != nil {
				operationErr = err
				break
			}
			if err = subject.publishTurnStopping(requestContext, turnNumber); err != nil {
				operationErr = err
				break
			}
			if err = contextFailure(requestContext); err != nil {
				operationErr = err
				break
			}
			if len(subject.pending.NextStep()) == 0 {
				break
			}
			if err = currentTurn.EnterOpenAfterStopping(); err != nil {
				operationErr = err
				break
			}
		}
		target = agent.NextStep
	}

	result := currentTurn.ResultValue()
	if operationErr != nil {
		if requestContext.Err() != nil {
			result = turnstate.TurnResultAborted
		} else {
			result = turnstate.TurnResultError
			subject.publishError(
				context.WithoutCancel(requestContext),
				turnNumber,
				currentTurn.LastStep(),
				operationErr,
			)
		}
	}
	if result == turnstate.TurnResultNone ||
		result == turnstate.TurnResultContinue {
		if operationErr == nil {
			operationErr = errors.New("agentloop: Turn ended without a result")
		}
		result = turnstate.TurnResultError
	}
	if currentTurn.StateValue() != turnstate.StateSettling {
		if err = currentTurn.EnterSettling(result); err != nil {
			operationErr = errors.Join(operationErr, err)
		}
	}
	settlementContext := context.WithoutCancel(requestContext)
	ending := subject.turnEndReason(result, operationErr)
	ended, endErr := session.NewEventDraft(
		session.TurnEnded,
		session.TurnEnd{
			Turn:   turnNumber,
			Reason: ending,
		},
	)
	if endErr == nil {
		_, endErr = subject.conversation.Commit(
			settlementContext,
			session.Batch(ended),
		)
	}
	operationErr = errors.Join(operationErr, endErr)
	flushErr := subject.sessions.Flush(settlementContext, subject.conversation)
	if flushErr != nil {
		flushErr = fmt.Errorf(
			"agentloop: flush Session %q after Turn %d: %w",
			subject.identifier,
			turnNumber,
			flushErr,
		)
		operationErr = errors.Join(operationErr, flushErr)
	}
	if endErr == nil && flushErr == nil {
		operationErr = errors.Join(operationErr, currentTurn.EnterClosed())
	}
	if operationErr != nil {
		return false, operationErr
	}
	return subject.pending.HasPending(), nil
}

func turnResultFromStep(result stepstate.StepResult) turnstate.TurnResult {
	switch result {
	case stepstate.StepResultContinue:
		return turnstate.TurnResultContinue
	case stepstate.StepResultCompleted:
		return turnstate.TurnResultCompleted
	case stepstate.StepResultMaxTokens:
		return turnstate.TurnResultMaxTokens
	case stepstate.StepResultAborted:
		return turnstate.TurnResultAborted
	case stepstate.StepResultError:
		return turnstate.TurnResultError
	default:
		return turnstate.TurnResultError
	}
}

func (subject *ReactLoopAgent) turnEndReason(
	result turnstate.TurnResult,
	problem error,
) session.TurnEndReason {
	switch result {
	case turnstate.TurnResultCompleted:
		return session.TurnCompleted{}
	case turnstate.TurnResultMaxTokens:
		return session.TurnMaxTokens{}
	case turnstate.TurnResultBlocked:
		return session.TurnBlocked{}
	case turnstate.TurnResultAborted:
		return session.TurnAborted{
			Reason: cancelReason(subject.execution.Snapshot().Cancellation),
		}
	case turnstate.TurnResultError:
		return session.TurnError{
			Error: failureFromError(problem),
		}
	default:
		return session.TurnError{
			Error: llm.LlmFailure{
				Message: "Turn ended without a terminal result",
				Code:    "UNKNOWN",
			},
		}
	}
}

func cancelReason(cancellation execution.Cancellation) session.TurnCancelCause {
	switch cancellation.Kind {
	case (agent.ParentCancel{}).CancelKind():
		return session.ParentCancelCause{}
	case (agent.DisposedCancel{}).CancelKind():
		return session.DisposedCancelCause{}
	case (agent.HookCancel{}).CancelKind():
		return session.HookCancelCause{
			Reason: cancellation.Reason,
		}
	case "", (agent.UserCancel{}).CancelKind():
		return session.UserCancelCause{}
	default:
		return session.HookCancelCause{
			Reason: cancellation.Kind,
		}
	}
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
