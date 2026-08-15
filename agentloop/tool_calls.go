package agentloop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/internal/jsonvalue"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/tools"
)

type plannedToolCall struct {
	block llm.ToolCallBlock
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

func (subject *ReactLoopAgent) executeToolCalls(
	requestContext context.Context,
	turn int64,
	step int64,
	blocks []llm.ToolCallBlock,
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
				CallID: block.ID, Name: block.Name, Arguments: arguments,
				Scope: subject.agentScope.Target(), Subject: subject,
			},
		}
	}
	nextIndex := 0
	concluded := false
	for nextIndex < len(plannedCalls) {
		first := plannedCalls[nextIndex]
		mode := subject.owner.tools.ExecutionMode(first.input)
		group := plannedCalls[nextIndex : nextIndex+1]
		if mode == tools.ExecutionParallel {
			group = plannedCalls[nextIndex:]
		}
		outcome, err := subject.runToolGroup(requestContext, turn, step, group, mode)
		if err != nil {
			return concluded, err
		}
		nextIndex += outcome.consumed
		concluded = concluded || outcome.concluded
		if outcome.aborted {
			for _, skipped := range plannedCalls[nextIndex:] {
				if err := subject.appendSkippedToolCall(turn, step, skipped.block); err != nil {
					return concluded, err
				}
			}
			return concluded, nil
		}
	}
	return concluded, nil
}

func (subject *ReactLoopAgent) runToolGroup(
	requestContext context.Context,
	turn int64,
	step int64,
	group []plannedToolCall,
	mode tools.ToolExecutionMode,
) (toolGroupOutcome, error) {
	scheduler := subject.owner.tools.Scheduler()
	if scheduler == nil {
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
				finalOutcome, finalizeErr = scheduler.Finalize(slot.execution, slot.outcome)
				if finalizeErr != nil {
					return finalizeErr
				}
			} else {
				finalOutcome = scheduler.Finish(slot.execution, slot.outcome)
			}
			if err := subject.appendToolResult(turn, step, group[committed].block, finalOutcome, callSequences[committed]); err != nil {
				return err
			}
			for _, additionalContext := range finalOutcome.AdditionalContextMessages() {
				if err := subject.pending.Append(agent.NextStep, additionalContext); err != nil {
					return err
				}
			}
			concluded = concluded || resultConcludesTurn(finalOutcome)
			committed++
		}
		return nil
	}

	startCall := func(index int) error {
		callSequence, err := subject.appendToolCall(turn, step, group[index].block)
		if err != nil {
			return err
		}
		callSequences[index] = callSequence
		started++
		preparation, err := scheduler.Prepare(requestContext, group[index].input)
		if err != nil {
			return err
		}
		switch preparation.Stage {
		case tools.ScheduledDispatch:
			inFlight[index] = struct{}{}
			go func() {
				settlement := dispatchSettlement{index: index}
				defer func() {
					if panicValue := recover(); panicValue != nil {
						settlement.err = fmt.Errorf("agentloop: tool scheduler dispatch panicked: %v", panicValue)
					}
					settled <- settlement
				}()
				dispatched, dispatchErr := scheduler.Dispatch(preparation.Execution)
				if dispatchErr != nil {
					settlement.err = dispatchErr
					return
				}
				settlement.slot = &settledToolSlot{
					execution: preparation.Execution, outcome: dispatched.Result, needsPost: dispatched.NeedsPost,
				}
			}()
		case tools.ScheduledPostResult:
			slots[index] = &settledToolSlot{
				execution: preparation.Execution, outcome: preparation.Result, needsPost: true,
			}
		case tools.ScheduledFinalResult:
			slots[index] = &settledToolSlot{
				execution: preparation.Execution, outcome: preparation.Result,
			}
		default:
			return fmt.Errorf("agentloop: unsupported tool preparation stage %q", preparation.Stage)
		}
		return nil
	}

	fillPool := func() error {
		for !aborted && nextToStart < len(group) && len(inFlight) < subject.owner.MaxParallelToolCalls() {
			if nextToStart > 0 && mode == tools.ExecutionParallel &&
				subject.owner.tools.ExecutionMode(group[nextToStart].input) != tools.ExecutionParallel {
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
			if err := subject.appendSkippedToolCall(turn, step, skipped.block); err != nil {
				return toolGroupOutcome{}, err
			}
		}
		return toolGroupOutcome{consumed: len(group), aborted: true, concluded: concluded}, nil
	}
	if committed != started {
		return toolGroupOutcome{}, errors.New("agentloop: settled tool calls were not committed in model order")
	}
	return toolGroupOutcome{consumed: started, concluded: concluded}, nil
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

func (subject *ReactLoopAgent) appendToolCall(turn int64, step int64, block llm.ToolCallBlock) (int64, error) {
	committed, err := session.Append(subject.conversation, session.ToolCalled, session.ToolCall{
		Turn: turn, Step: step, CallID: block.ID, Name: block.Name, Arguments: block.Arguments,
	})
	return committed.Seq, err
}

func (subject *ReactLoopAgent) appendSkippedToolCall(turn int64, step int64, block llm.ToolCallBlock) error {
	callSequence, err := subject.appendToolCall(turn, step, block)
	if err != nil {
		return err
	}
	failure := &tools.ToolExecutionFailure{
		Error: tools.ToolFailure{
			Message: "tool call aborted before dispatch",
			Info:    &tools.ToolErrorInfo{Name: "AbortError", Code: tools.ToolAbortedBeforeDispatch},
		},
		Content: []llm.ContentBlock{llm.NewTextBlock("Error: tool call aborted before dispatch")},
	}
	return subject.appendToolResult(turn, step, block, failure, callSequence)
}

func (subject *ReactLoopAgent) appendToolResult(
	turn int64,
	step int64,
	block llm.ToolCallBlock,
	outcome tools.ToolExecutionResult,
	callSequence int64,
) error {
	if outcome == nil {
		return errors.New("agentloop: Tool scheduler returned a nil result")
	}
	toolReply, err := llm.NewToolResultMessage(llm.ToolResultMessageInput{
		CallID: block.ID, Content: outcome.ContentBlocks(), IsError: outcome.Failed(),
	})
	if err != nil {
		return err
	}
	payload := session.ToolResult{
		Turn: turn, Step: step, Message: toolReply, Meta: outcomeMeta(outcome),
	}
	if failure, present := outcomeFailure(outcome); present && failure.Info != nil {
		payload.Error = &session.ToolErrorInfo{Name: failure.Info.Name, Code: failure.Info.Code}
	}
	provenance := []int64{callSequence}
	_, err = session.AppendSurface(subject.conversation, session.ToolResultAdded, payload, session.SurfaceIntent{
		Operation: session.SurfaceAppend(), SourceEventSeqs: &provenance,
	})
	return err
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
