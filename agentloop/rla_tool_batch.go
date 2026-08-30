package agentloop

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentloop/internal/toolbatch"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/tools"
)

var _ toolGroupAgent = (*ReactLoopAgent)(nil)

// commitSession is the Session write capability RLA offers to Tool group
// execution. The collaborator receives behavior, not the mutable Session
// object retained by RLA.
func (subject *ReactLoopAgent) commitSession(
	requestContext context.Context,
	plan session.WritePlan,
) (session.WriteResult, error) {
	return subject.conversation.Commit(requestContext, plan)
}

// appendNextStep is the Inbox capability RLA offers to Tool group execution.
// It fixes the target so the collaborator cannot manipulate unrelated queues.
func (subject *ReactLoopAgent) appendNextStep(
	message agentmessage.UserMessage,
) error {
	return subject.pending.Append(agent.NextStep, message)
}

// plannedToolCall is one model-order Tool call with parsed execution input.
type plannedToolCall struct {
	block agentmessage.ToolCallBlock
	input tools.ToolExecutionInput
}

// executeToolBatch maps one Assistant Tool-call list into ToolBatch state and
// delegates each execution-mode group without exposing RLA internals.
func (subject *ReactLoopAgent) executeToolBatch(
	requestContext context.Context,
	turnNumber int64,
	stepNumber int64,
	blocks []agentmessage.ToolCallBlock,
) (bool, error) {
	if len(blocks) == 0 {
		return false, errors.New("agentloop: ToolBatch requires at least one call")
	}
	plannedCalls := make([]plannedToolCall, len(blocks))
	for index, block := range blocks {
		arguments, err := parseToolArguments(block.Arguments)
		if err != nil {
			return false, err
		}
		plannedCalls[index] = plannedToolCall{
			block: block,
			input: tools.ToolExecutionInput{
				CallID:    block.ID,
				Name:      block.Name,
				Arguments: arguments,
				Subject:   subject,
			},
		}
	}
	batch, err := toolbatch.New(len(plannedCalls), subject.maxParallelToolCalls)
	if err != nil {
		return false, err
	}
	if err = batch.EnterDispatching(); err != nil {
		return false, err
	}
	nextIndex := 0
	for nextIndex < len(plannedCalls) {
		mode := subject.toolRuntime.ExecutionMode(plannedCalls[nextIndex].input)
		groupEnd := nextIndex + 1
		if mode == tools.ExecutionParallel {
			for groupEnd < len(plannedCalls) &&
				subject.toolRuntime.ExecutionMode(
					plannedCalls[groupEnd].input,
				) == tools.ExecutionParallel {
				groupEnd++
			}
		}
		groupExecution, groupErr := newToolGroupExecution(
			subject,
			subject.maxParallelToolCalls,
			batch,
			subject.toolRuntime.Scheduler(),
			requestContext,
			turnNumber,
			stepNumber,
			nextIndex,
			plannedCalls[nextIndex:groupEnd],
			mode,
		)
		if groupErr != nil {
			return batch.StopsModelContinuation(), groupErr
		}
		groupResult, groupErr := groupExecution.run()
		nextIndex += groupResult.processed
		if groupErr != nil {
			drainReason := toolbatch.DrainFailure
			if requestContext.Err() != nil {
				drainReason = toolbatch.DrainCancellation
			}
			if batch.StateValue() == toolbatch.StateDispatching {
				_ = batch.EnterDraining(drainReason)
			}
			if drainReason == toolbatch.DrainCancellation {
				settlementContext := context.WithoutCancel(requestContext)
				for index := nextIndex; index < len(plannedCalls); index++ {
					if appendErr := appendSkippedToolCall(
						subject,
						settlementContext,
						turnNumber,
						stepNumber,
						plannedCalls[index].block,
					); appendErr != nil {
						groupErr = errors.Join(groupErr, appendErr)
						break
					}
					if stateErr := batch.RecordSkippedResult(index); stateErr != nil {
						groupErr = errors.Join(groupErr, stateErr)
						break
					}
				}
			}
			groupErr = errors.Join(groupErr, batch.EnterSettling())
			if batch.StateValue() == toolbatch.StateSettling {
				groupErr = errors.Join(groupErr, batch.EnterClosed())
			}
			return batch.StopsModelContinuation(), groupErr
		}
		if groupResult.canceled {
			settlementContext := context.WithoutCancel(requestContext)
			for index := nextIndex; index < len(plannedCalls); index++ {
				if err = appendSkippedToolCall(
					subject,
					settlementContext,
					turnNumber,
					stepNumber,
					plannedCalls[index].block,
				); err != nil {
					return batch.StopsModelContinuation(), err
				}
				if err = batch.RecordSkippedResult(index); err != nil {
					return batch.StopsModelContinuation(), err
				}
			}
			if err = batch.EnterSettling(); err == nil {
				err = batch.EnterClosed()
			}
			return batch.StopsModelContinuation(), errors.Join(
				contextFailure(requestContext),
				err,
			)
		}
	}
	if err = batch.EnterSettling(); err == nil {
		err = batch.EnterClosed()
	}
	return batch.StopsModelContinuation(), err
}
