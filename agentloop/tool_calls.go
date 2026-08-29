package agentloop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/internal/jsonvalue"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/tools"
)

// toolCallExecutor owns one step's bounded Tool-call scheduling, started-call
// drain, and model-order result commit.
type toolCallExecutor struct {
	subject              *ReactLoopAgent
	pending              *agent.Inbox
	toolRuntime          tools.ToolRuntime
	maxParallelToolCalls int
}

func newToolCallExecutor(
	subject *ReactLoopAgent,
	pending *agent.Inbox,
	toolRuntime tools.ToolRuntime,
	maxParallelToolCalls int,
) (*toolCallExecutor, error) {
	if subject == nil || pending == nil || toolRuntime == nil {
		return nil, errors.New("agentloop: Tool-call dependencies are incomplete")
	}
	if maxParallelToolCalls < 1 {
		return nil, errors.New("agentloop: Tool-call concurrency must be positive")
	}
	return &toolCallExecutor{
		subject:              subject,
		pending:              pending,
		toolRuntime:          toolRuntime,
		maxParallelToolCalls: maxParallelToolCalls,
	}, nil
}

func (executor *toolCallExecutor) deactivate() {
	executor.toolRuntime = nil
}

type plannedToolCall struct {
	block agentmessage.ToolCallBlock
	input tools.ToolExecutionInput
}

type settledToolSlot struct {
	execution tools.ToolExecution
	outcome   tools.ToolExecutionResult
	needsPost bool
}

type toolGroupOutcome struct {
	consumed  int
	aborted   bool
	concluded bool
}

type dispatchSettlement struct {
	index int
	slot  *settledToolSlot
	err   error
}

func (executor *toolCallExecutor) execute(
	requestContext context.Context,
	turn int64,
	step int64,
	blocks []agentmessage.ToolCallBlock,
) (bool, error) {
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
				Subject:   executor.subject,
			},
		}
	}
	nextIndex := 0
	concluded := false
	for nextIndex < len(plannedCalls) {
		first := plannedCalls[nextIndex]
		mode := executor.toolRuntime.ExecutionMode(first.input)
		group := plannedCalls[nextIndex : nextIndex+1]
		if mode == tools.ExecutionParallel {
			group = plannedCalls[nextIndex:]
		}
		outcome, err := executor.runToolGroup(
			requestContext,
			turn,
			step,
			group,
			mode,
		)
		if err != nil {
			return concluded, err
		}
		nextIndex += outcome.consumed
		concluded = concluded || outcome.concluded
		if outcome.aborted {
			for _, skipped := range plannedCalls[nextIndex:] {
				if err := executor.appendSkippedToolCall(
					requestContext,
					turn,
					step,
					skipped.block,
				); err != nil {
					return concluded, err
				}
			}
			return concluded, nil
		}
	}
	return concluded, nil
}

func (executor *toolCallExecutor) runToolGroup(
	requestContext context.Context,
	turn int64,
	step int64,
	group []plannedToolCall,
	mode tools.ToolExecutionMode,
) (toolGroupOutcome, error) {
	executionScheduler := executor.toolRuntime.Scheduler()
	if executionScheduler == nil {
		return toolGroupOutcome{}, errors.New("agentloop: Tools runtime returned a nil scheduler")
	}
	slots := make([]*settledToolSlot, len(group))
	callSequences := make([]int64, len(group))
	settled := make(chan dispatchSettlement, len(group))
	inFlight := make(map[int]struct{})
	nextToStart := 0
	committed := 0
	started := 0
	aborted := requestContext.Err() != nil
	concluded := false

	commitReady := func() error {
		for committed < len(group) {
			slot := slots[committed]
			if slot == nil {
				return nil
			}
			finalOutcome := slot.outcome
			if slot.needsPost {
				var finalizeErr error
				finalOutcome, finalizeErr = executionScheduler.Finalize(
					slot.execution,
					slot.outcome,
				)
				if finalizeErr != nil {
					return finalizeErr
				}
			} else {
				finalOutcome = executionScheduler.Finish(
					slot.execution,
					slot.outcome,
				)
			}
			if err := executor.appendToolResult(
				requestContext,
				turn,
				step,
				group[committed].block,
				finalOutcome,
				callSequences[committed],
			); err != nil {
				return err
			}
			for _, additionalContext := range finalOutcome.AdditionalContextMessages() {
				if err := executor.pending.Append(agent.NextStep, additionalContext); err != nil {
					return err
				}
			}
			concluded = concluded || resultConcludesTurn(finalOutcome)
			committed++
		}
		return nil
	}

	startCall := func(index int) error {
		callSequence, err := executor.appendToolCall(
			requestContext,
			turn,
			step,
			group[index].block,
		)
		if err != nil {
			return err
		}
		callSequences[index] = callSequence
		started++
		preparation, err := executionScheduler.Prepare(
			requestContext,
			group[index].input,
		)
		if err != nil {
			return err
		}
		switch preparation.Stage {
		case tools.ScheduledDispatch:
			inFlight[index] = struct{}{}
			go func() {
				settlement := dispatchSettlement{
					index: index,
				}
				defer func() {
					if panicValue := recover(); panicValue != nil {
						settlement.err = fmt.Errorf("agentloop: tool scheduler dispatch panicked: %v", panicValue)
					}
					settled <- settlement
				}()
				dispatched, dispatchErr := executionScheduler.Dispatch(
					preparation.Execution,
				)
				if dispatchErr != nil {
					settlement.err = dispatchErr
					return
				}
				settlement.slot = &settledToolSlot{
					execution: preparation.Execution,
					outcome:   dispatched.Result,
					needsPost: true,
				}
			}()
		case tools.ScheduledPostResult:
			slots[index] = &settledToolSlot{
				execution: preparation.Execution,
				outcome:   preparation.Result,
				needsPost: true,
			}
		case tools.ScheduledFinalResult:
			slots[index] = &settledToolSlot{
				execution: preparation.Execution,
				outcome:   preparation.Result,
			}
		default:
			return fmt.Errorf("agentloop: unsupported tool preparation stage %q", preparation.Stage)
		}
		return nil
	}

	fillPool := func() error {
		for !aborted && nextToStart < len(group) &&
			len(inFlight) < executor.maxParallelToolCalls {
			if nextToStart > 0 && mode == tools.ExecutionParallel &&
				executor.toolRuntime.ExecutionMode(group[nextToStart].input) != tools.ExecutionParallel {
				break
			}
			if err := startCall(nextToStart); err != nil {
				return err
			}
			nextToStart++
			if err := commitReady(); err != nil {
				return err
			}
			aborted = requestContext.Err() != nil
		}
		return nil
	}

	var schedulerErr error
	if err := fillPool(); err != nil {
		schedulerErr = err
	}
	for len(inFlight) != 0 {
		settlement := <-settled
		delete(inFlight, settlement.index)
		if settlement.err != nil && schedulerErr == nil {
			schedulerErr = settlement.err
		} else if settlement.err == nil {
			slots[settlement.index] = settlement.slot
		}
		if schedulerErr == nil {
			if err := commitReady(); err != nil {
				schedulerErr = err
			}
		}
		aborted = aborted || requestContext.Err() != nil
		if schedulerErr == nil {
			if err := fillPool(); err != nil {
				schedulerErr = err
			}
		}
	}
	if schedulerErr != nil {
		return toolGroupOutcome{}, schedulerErr
	}
	if aborted {
		for _, skipped := range group[started:] {
			if err := executor.appendSkippedToolCall(
				requestContext,
				turn,
				step,
				skipped.block,
			); err != nil {
				return toolGroupOutcome{}, err
			}
		}
		return toolGroupOutcome{
			consumed:  len(group),
			aborted:   true,
			concluded: concluded,
		}, nil
	}
	if committed != started {
		return toolGroupOutcome{}, errors.New("agentloop: settled tool calls were not committed in model order")
	}
	return toolGroupOutcome{
		consumed:  started,
		concluded: concluded,
	}, nil
}

func parseToolArguments(rawValue string) (json.RawMessage, error) {
	if rawValue == "" {
		return json.RawMessage(`{}`), nil
	}
	candidate := json.RawMessage(rawValue)
	if jsonvalue.Validate(candidate) == nil {
		return append(json.RawMessage(nil), candidate...), nil
	}
	encoded, err := json.Marshal(rawValue)
	if err != nil {
		return nil, fmt.Errorf("agentloop: preserve invalid tool arguments: %w", err)
	}
	return json.RawMessage(encoded), nil
}

func (executor *toolCallExecutor) appendToolCall(
	requestContext context.Context,
	turn int64,
	step int64,
	block agentmessage.ToolCallBlock,
) (int64, error) {
	draft, err := session.NewEventDraft(
		session.ToolCalled,
		session.ToolCall{
			Turn:      turn,
			Step:      step,
			CallID:    block.ID,
			Name:      block.Name,
			Arguments: block.Arguments,
		},
	)
	if err != nil {
		return 0, err
	}
	result, err := executor.subject.conversation.Commit(requestContext, session.Batch(draft))
	if err != nil {
		return 0, err
	}
	return result.Events[0].Seq, nil
}

func (executor *toolCallExecutor) appendSkippedToolCall(
	requestContext context.Context,
	turn int64,
	step int64,
	block agentmessage.ToolCallBlock,
) error {
	failure := &tools.ToolExecutionFailure{
		Error: tools.ToolFailure{
			Message: "tool call aborted before dispatch",
			Info: &tools.ToolErrorInfo{
				Name: "AbortError",
				Code: tools.ToolAbortedBeforeDispatch,
			},
		},
		Content: []agentmessage.ContentBlock{
			agentmessage.NewTextBlock("Error: tool call aborted before dispatch"),
		},
	}
	toolReply, err := toolResultPayload(turn, step, block, failure)
	if err != nil {
		return err
	}
	callDraft, err := session.NewEventDraft(
		session.ToolCalled,
		session.ToolCall{
			Turn:      turn,
			Step:      step,
			CallID:    block.ID,
			Name:      block.Name,
			Arguments: block.Arguments,
		},
	)
	if err != nil {
		return err
	}
	_, err = executor.subject.conversation.Commit(
		requestContext,
		&skippedCallPlan{
			callDraft: callDraft,
			result:    toolReply,
		},
	)
	return err
}

func (executor *toolCallExecutor) appendToolResult(
	requestContext context.Context,
	turn int64,
	step int64,
	block agentmessage.ToolCallBlock,
	outcome tools.ToolExecutionResult,
	callSequence int64,
) error {
	payload, err := toolResultPayload(turn, step, block, outcome)
	if err != nil {
		return err
	}
	provenance := []int64{callSequence}
	draft, err := session.NewSurfaceEventDraft(
		session.ToolResultAdded,
		payload,
		session.SurfaceIntent{
			Operation:       session.SurfaceAppend(),
			SourceEventSeqs: &provenance,
		},
	)
	if err != nil {
		return err
	}
	_, err = executor.subject.conversation.Commit(requestContext, session.Batch(draft))
	return err
}

func toolResultPayload(
	turn int64,
	step int64,
	block agentmessage.ToolCallBlock,
	outcome tools.ToolExecutionResult,
) (session.ToolResult, error) {
	if outcome == nil {
		return session.ToolResult{}, errors.New("agentloop: Tool scheduler returned a nil result")
	}
	toolReply, err := agentmessage.NewToolResultMessage(agentmessage.ToolResultMessageInput{
		CallID:  block.ID,
		Content: outcome.ContentBlocks(),
		IsError: outcome.Failed(),
	})
	if err != nil {
		return session.ToolResult{}, err
	}
	payload := session.ToolResult{
		Turn:    turn,
		Step:    step,
		Message: toolReply,
		Meta:    outcomeMeta(outcome),
	}
	if failure, present := outcomeFailure(outcome); present && failure.Info != nil {
		payload.Error = &session.ToolErrorInfo{
			Name: failure.Info.Name,
			Code: failure.Info.Code,
		}
	}
	return payload, nil
}

type skippedCallPlan struct {
	callDraft session.EventDraft
	result    session.ToolResult
}

func (plan *skippedCallPlan) Build(
	_ context.Context,
	current session.Snapshot,
) ([]session.EventDraft, error) {
	provenance := []int64{current.Barrier.NextSeq}
	resultDraft, err := session.NewSurfaceEventDraft(
		session.ToolResultAdded,
		plan.result,
		session.SurfaceIntent{
			Operation:       session.SurfaceAppend(),
			SourceEventSeqs: &provenance,
		},
	)
	if err != nil {
		return nil, err
	}
	return []session.EventDraft{
		plan.callDraft,
		resultDraft,
	}, nil
}

func outcomeFailure(outcome tools.ToolExecutionResult) (tools.ToolFailure, bool) {
	failureOutcome, ok := outcome.(*tools.ToolExecutionFailure)
	if !ok || failureOutcome == nil {
		return tools.ToolFailure{}, false
	}
	return failureOutcome.Error, true
}

func outcomeMeta(outcome tools.ToolExecutionResult) json.RawMessage {
	switch retained := outcome.(type) {
	case *tools.ToolExecutionSuccess:
		return append(json.RawMessage(nil), retained.Meta...)
	case *tools.ToolExecutionFailure:
		return append(json.RawMessage(nil), retained.Meta...)
	default:
		return nil
	}
}

func resultConcludesTurn(outcome tools.ToolExecutionResult) bool {
	succeeded, ok := outcome.(*tools.ToolExecutionSuccess)
	return ok && succeeded != nil && succeeded.ConcludesTurn
}
