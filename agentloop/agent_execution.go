package agentloop

import (
	"errors"

	"github.com/gorenx/goren/agent"
	stepstate "github.com/gorenx/goren/agentloop/internal/step"
	"github.com/gorenx/goren/session"
)

// Run starts accepted execution using asynchronous capabilities. Existing Turns
// and maintenance already have their own continuation and cannot be started a
// second time by another Inbox wakeup.
func (subject *ReactLoopAgent) Run(services Services) error {
	if services.Conversation == nil ||
		services.Prompts == nil ||
		services.Middleware == nil ||
		services.Models == nil ||
		services.Observer == nil {
		return errors.New("agentloop: execution capabilities are incomplete")
	}
	if subject.state != executionRunning {
		return nil
	}
	if subject.retry != nil {
		retry := subject.retry
		subject.retry = nil
		retry.resume(subject, services)
		return nil
	}
	if subject.current != nil {
		return nil
	}
	subject.startTurn(services)
	return nil
}

// MaintenanceCompleted resumes accepted input only after the maintenance owner
// confirms completion. Cancellation acknowledgement is not completion.
func (subject *ReactLoopAgent) MaintenanceCompleted(services Services) error {
	replay, err := subject.FinishMaintenance()
	if err != nil {
		return err
	}
	if replay {
		return subject.Run(services)
	}
	services.Observer.ExecutionFinished()
	return nil
}

func (work *ReactLoopAgent) startTurn(services Services) {
	// Cancellation before a durable Turn exists releases accepted execution.
	// There is no Turn to settle, and preserved input remains in the Inbox.
	if work.current == nil && work.Cancellation() != nil {
		repeat, err := work.FinishExecution()
		if err != nil {
			services.Observer.ExecutionFailed(err)
			return
		}
		if repeat {
			work.startTurn(services)
			return
		}
		services.Observer.ExecutionFinished()
		return
	}
	beginning, err := work.PrepareTurn()
	if err != nil {
		services.Observer.ExecutionFailed(err)
		return
	}
	opening := &turnOpening{
		work: work,
		fact: beginning,
	}
	err = services.Conversation.Commit(boundaryWrite{fact: opening.fact}, opening)
	if err != nil {
		opening.SessionCommitted(services, session.WriteResult{}, err)
	}
}

// turnOpening receives acknowledgement of the independently submitted immutable fact.
type turnOpening struct {
	work *ReactLoopAgent
	fact TurnStart
}

func (opening *turnOpening) SessionCommitted(services Services, result session.WriteResult, failure error) {
	if failure == nil {
		failure = opening.work.ConfirmTurn(result)
	}
	if failure != nil {
		opening.work.retry = startRetry{fact: opening.fact}
		services.Observer.ExecutionFailed(failure)
		return
	}
	opening.work.prepareStep(services)
}

func (work *ReactLoopAgent) continueTurn(services Services) {
	next, err := work.ContinueTurn(work.failure)
	if err != nil {
		services.Observer.ExecutionFailed(err)
		return
	}
	work.continueAfterStep(services, next)
}

func (work *ReactLoopAgent) continueAfterStep(services Services, next TurnOperation) {
	switch selected := next.(type) {
	case ContinueSteps:
		work.prepareStep(services)
	case RunTurnStopping:
		err := services.Middleware.TurnStopping(selected.Input, work)
		if err != nil {
			work.TurnStoppingCompleted(services, agent.TurnStoppingCompleted{Err: err})
		}
	case TurnEnd:
		ending := &turnClosure{
			work: work,
			fact: selected,
		}
		err := services.Conversation.Commit(boundaryWrite{fact: ending.fact}, ending)
		if err != nil {
			ending.SessionCommitted(services, session.WriteResult{}, err)
		}
	default:
		services.Observer.ExecutionFailed(errors.New("agentloop: invalid Turn continuation"))
	}
}

func (work *ReactLoopAgent) TurnStoppingCompleted(services Services, result agent.TurnStoppingCompleted) {
	next, err := work.CompleteStopping(result)
	if err != nil {
		services.Observer.ExecutionFailed(err)
		return
	}
	work.continueAfterStep(services, next)
}

type stepClosure struct {
	work *ReactLoopAgent
	fact EndStep
}

func (ending *stepClosure) SessionCommitted(services Services, result session.WriteResult, failure error) {
	if failure == nil {
		failure = ending.work.ConfirmStepEnd(result)
	}
	if failure != nil {
		ending.work.retry = stepRetry{fact: ending.fact}
		services.Observer.ExecutionFailed(failure)
		return
	}
	ending.work.failure = errors.Join(ending.work.failure, ending.fact.Err)
	ending.work.continueTurn(services)
}

func (work *ReactLoopAgent) commitStepEnd(services Services, fact EndStep) {
	ending := &stepClosure{
		work: work,
		fact: fact,
	}
	err := services.Conversation.Commit(boundaryWrite{fact: ending.fact}, ending)
	if err != nil {
		ending.SessionCommitted(services, session.WriteResult{}, err)
	}
}

type turnClosure struct {
	work *ReactLoopAgent
	fact TurnEnd
}

func (ending *turnClosure) SessionCommitted(services Services, result session.WriteResult, failure error) {
	if failure == nil {
		failure = ending.work.ConfirmTurnEnd(result)
	}
	if failure != nil {
		ending.work.retry = turnRetry{fact: ending.fact}
		services.Observer.ExecutionFailed(failure)
		return
	}
	barrier, err := ending.work.TurnBarrier()
	if err != nil {
		services.Observer.ExecutionFailed(err)
		return
	}
	err = services.Conversation.Flush(barrier, ending.work)
	if err != nil {
		ending.work.SessionFlushed(services, session.WriteBarrier{}, err)
	}
}

func (work *ReactLoopAgent) SessionFlushed(services Services, durable session.WriteBarrier, failure error) {
	if failure == nil {
		failure = work.ConfirmTurnFlush(durable)
	}
	if failure != nil {
		work.retry = flushRetry{}
		services.Observer.ExecutionFailed(failure)
		return
	}
	conclusion := work.failure
	repeat, err := work.ContinueExecution()
	if err != nil {
		services.Observer.ExecutionFailed(err)
		return
	}
	if conclusion != nil {
		services.Observer.ExecutionFailed(conclusion)
	}
	if repeat {
		work.startTurn(services)
		return
	}
	services.Observer.ExecutionFinished()
}

// executionRetry preserves a failed boundary without capturing the owner. The
// business caller may retry through Run; ordinary in-flight wakes remain inert.
type executionRetry interface {
	resume(*ReactLoopAgent, Services)
}

type startRetry struct{ fact TurnStart }

func (retry startRetry) resume(subject *ReactLoopAgent, services Services) {
	opening := &turnOpening{
		work: subject,
		fact: retry.fact,
	}
	err := services.Conversation.Commit(boundaryWrite{fact: retry.fact}, opening)
	if err != nil {
		opening.SessionCommitted(services, session.WriteResult{}, err)
	}
}

type stepRetry struct{ fact EndStep }

func (retry stepRetry) resume(subject *ReactLoopAgent, services Services) {
	subject.commitStepEnd(services, retry.fact)
}

type turnRetry struct{ fact TurnEnd }

func (retry turnRetry) resume(subject *ReactLoopAgent, services Services) {
	subject.continueAfterStep(services, retry.fact)
}

type flushRetry struct{}

func (flushRetry) resume(subject *ReactLoopAgent, services Services) {
	barrier, err := subject.TurnBarrier()
	if err != nil {
		services.Observer.ExecutionFailed(err)
		return
	}
	err = services.Conversation.Flush(barrier, subject)
	if err != nil {
		subject.SessionFlushed(services, session.WriteBarrier{}, err)
	}
}

func (work *ReactLoopAgent) failStep(services Services, failure error) {
	work.failure = errors.Join(work.failure, failure)
	if work.preparing != nil {
		err := work.AbandonPreparation()
		if err != nil {
			services.Observer.ExecutionFailed(errors.Join(work.failure, err))
			return
		}
	}
	ending, err := work.CompleteStepFailure(failure)
	if err != nil {
		services.Observer.ExecutionFailed(errors.Join(work.failure, err))
		return
	}
	if ending != nil {
		work.commitStepEnd(services, *ending)
		return
	}
	work.continueTurn(services)
}

// CompleteStepFailure selects an error or cancellation conclusion for an opened
// Step. Without a Step, the caller must settle the existing Turn instead.
func (subject *ReactLoopAgent) CompleteStepFailure(failure error) (*EndStep, error) {
	if failure == nil {
		return nil, errors.New("agentloop: Step failure is required")
	}
	if subject.step == nil {
		return nil, nil
	}
	conclusion := stepstate.StepResultError
	var cancelled agent.Cancellation
	if errors.As(failure, &cancelled) {
		conclusion = stepstate.StepResultAborted
	}
	ending, err := subject.endStep(conclusion, failure)
	if err != nil {
		return nil, err
	}
	return &ending, nil
}
