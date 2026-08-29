package agentloop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gorenx/goren/agentloop/internal/toolbatch"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/internal/jsonvalue"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/tools"
)

// settledToolCall is one Tool body result waiting for model-order commit.
type settledToolCall struct {
	execution      tools.ToolExecution
	result         tools.ToolExecutionResult
	finalize       bool
	acceptDirectly bool
}

// toolGroupResult describes how much of one execution-mode group was consumed.
type toolGroupResult struct {
	processed int
	canceled  bool
}

// toolDispatchResult is the completion record of one asynchronous Tool body.
type toolDispatchResult struct {
	index int
	call  *settledToolCall
	err   error
}

// toolGroupAgent is the consumer-owned RLA view required by Tool group
// execution. ReactLoopAgent satisfies it without exposing its concrete fields.
type toolGroupAgent interface {
	commitSession(context.Context, session.WritePlan) (session.WriteResult, error)
	appendNextStep(agentmessage.UserMessage) error
}

// toolGroupExecution is one short-lived execution-mode group. It owns
// scheduling progress and never retains or exposes ReactLoopAgent.
type toolGroupExecution struct {
	subject        toolGroupAgent
	parallelLimit  int
	batch          *toolbatch.ToolBatch
	scheduler      tools.ToolExecutionScheduler
	requestContext context.Context
	turnNumber     int64
	stepNumber     int64
	baseIndex      int
	calls          []plannedToolCall
	mode           tools.ToolExecutionMode
	settledCalls   []*settledToolCall
	callSequences  []int64
	dispatched     chan toolDispatchResult
	// inFlight is the exact set of Tool bodies currently executing. The key is
	// the group-local call index. The empty value is a membership marker:
	// presence means Dispatch has not settled; absence means it never started or
	// has already settled.
	inFlight     map[int]struct{}
	nextToStart  int
	nextToCommit int
	started      int
	canceled     bool
}

func newToolGroupExecution(
	subject toolGroupAgent,
	parallelLimit int,
	batch *toolbatch.ToolBatch,
	toolScheduler tools.ToolExecutionScheduler,
	requestContext context.Context,
	turnNumber int64,
	stepNumber int64,
	baseIndex int,
	calls []plannedToolCall,
	mode tools.ToolExecutionMode,
) (*toolGroupExecution, error) {
	if subject == nil || batch == nil || toolScheduler == nil {
		return nil, errors.New(
			"agentloop: Tool group dependencies are incomplete",
		)
	}
	if requestContext == nil || parallelLimit < 1 || len(calls) == 0 {
		return nil, errors.New(
			"agentloop: Tool group execution parameters are invalid",
		)
	}
	return &toolGroupExecution{
		subject:        subject,
		parallelLimit:  parallelLimit,
		batch:          batch,
		scheduler:      toolScheduler,
		requestContext: requestContext,
		turnNumber:     turnNumber,
		stepNumber:     stepNumber,
		baseIndex:      baseIndex,
		calls:          calls,
		mode:           mode,
		settledCalls:   make([]*settledToolCall, len(calls)),
		callSequences:  make([]int64, len(calls)),
		dispatched:     make(chan toolDispatchResult, len(calls)),
		inFlight:       make(map[int]struct{}),
		canceled:       requestContext.Err() != nil,
	}, nil
}

func (current *toolGroupExecution) run() (toolGroupResult, error) {
	schedulerErr := current.fillPool()
	for len(current.inFlight) != 0 {
		dispatchResult := <-current.dispatched
		delete(current.inFlight, dispatchResult.index)
		globalIndex := current.baseIndex + dispatchResult.index
		if stateErr := current.batch.RecordCallSettlement(globalIndex); stateErr != nil &&
			schedulerErr == nil {
			schedulerErr = stateErr
		}
		if dispatchResult.err != nil && schedulerErr == nil {
			schedulerErr = dispatchResult.err
		} else if dispatchResult.err == nil {
			current.settledCalls[dispatchResult.index] = dispatchResult.call
		}
		if schedulerErr == nil {
			schedulerErr = current.commitReadyResults()
		}
		current.canceled = current.canceled ||
			current.requestContext.Err() != nil
		if schedulerErr == nil {
			schedulerErr = current.fillPool()
		}
	}
	if schedulerErr != nil {
		return toolGroupResult{
			processed: current.started,
		}, schedulerErr
	}
	if current.canceled {
		if current.batch.StateValue() == toolbatch.StateDispatching {
			if err := current.batch.EnterDraining(
				toolbatch.DrainCancellation,
			); err != nil {
				return toolGroupResult{}, err
			}
		}
		settlementContext := context.WithoutCancel(current.requestContext)
		for index := current.started; index < len(current.calls); index++ {
			if err := appendSkippedToolCall(
				current.subject,
				settlementContext,
				current.turnNumber,
				current.stepNumber,
				current.calls[index].block,
			); err != nil {
				return toolGroupResult{}, err
			}
			if err := current.batch.RecordSkippedResult(
				current.baseIndex + index,
			); err != nil {
				return toolGroupResult{}, err
			}
		}
		return toolGroupResult{
			processed: len(current.calls),
			canceled:  true,
		}, nil
	}
	if current.nextToCommit != current.started {
		return toolGroupResult{}, errors.New(
			"agentloop: settled Tool calls were not committed in model order",
		)
	}
	return toolGroupResult{
		processed: current.started,
	}, nil
}

func (current *toolGroupExecution) fillPool() error {
	for !current.canceled && current.nextToStart < len(current.calls) &&
		len(current.inFlight) < current.parallelLimit {
		if current.nextToStart > 0 && current.mode != tools.ExecutionParallel {
			break
		}
		if err := current.startNext(); err != nil {
			return err
		}
		if err := current.commitReadyResults(); err != nil {
			return err
		}
		current.canceled = current.requestContext.Err() != nil
	}
	return nil
}

func (current *toolGroupExecution) startNext() error {
	index := current.nextToStart
	callSequence, err := current.appendToolCall(current.calls[index].block)
	if err != nil {
		return err
	}
	globalIndex := current.baseIndex + index
	if err = current.batch.RecordCallStart(globalIndex); err != nil {
		return err
	}
	current.callSequences[index] = callSequence
	current.started++
	current.nextToStart++
	preparation, err := current.scheduler.Prepare(
		current.requestContext,
		current.calls[index].input,
	)
	if err != nil {
		stateErr := current.batch.RecordCallSettlement(globalIndex)
		if current.requestContext.Err() != nil && stateErr == nil {
			current.settledCalls[index] = &settledToolCall{
				result:         abortedBeforeDispatchResult(),
				acceptDirectly: true,
			}
			current.canceled = true
			return current.commitReadyResults()
		}
		if stateErr != nil {
			return errors.Join(err, stateErr)
		}
		return err
	}
	switch preparation.Stage {
	case tools.ScheduledDispatch:
		current.inFlight[index] = struct{}{}
		go current.dispatch(index, preparation.Execution)
	case tools.ScheduledPostResult:
		current.settledCalls[index] = &settledToolCall{
			execution: preparation.Execution,
			result:    preparation.Result,
			finalize:  true,
		}
		return current.batch.RecordCallSettlement(globalIndex)
	case tools.ScheduledFinalResult:
		current.settledCalls[index] = &settledToolCall{
			execution: preparation.Execution,
			result:    preparation.Result,
		}
		return current.batch.RecordCallSettlement(globalIndex)
	default:
		_ = current.batch.RecordCallSettlement(globalIndex)
		return fmt.Errorf(
			"agentloop: unsupported Tool preparation stage %q",
			preparation.Stage,
		)
	}
	return nil
}

func (current *toolGroupExecution) dispatch(
	index int,
	execution tools.ToolExecution,
) {
	dispatchValue := toolDispatchResult{
		index: index,
	}
	defer func() {
		if panicValue := recover(); panicValue != nil {
			dispatchValue.err = fmt.Errorf(
				"agentloop: Tool Scheduler Dispatch panicked: %v",
				panicValue,
			)
		}
		current.dispatched <- dispatchValue
	}()
	dispatched, err := current.scheduler.Dispatch(execution)
	if err != nil {
		dispatchValue.err = err
		return
	}
	dispatchValue.call = &settledToolCall{
		execution: execution,
		result:    dispatched.Result,
		finalize:  true,
	}
}

func (current *toolGroupExecution) commitReadyResults() error {
	for current.nextToCommit < len(current.calls) {
		settledCall := current.settledCalls[current.nextToCommit]
		if settledCall == nil {
			return nil
		}
		finalResult := settledCall.result
		if settledCall.acceptDirectly {
			finalResult = settledCall.result
		} else if settledCall.finalize {
			var err error
			finalResult, err = current.scheduler.Finalize(
				settledCall.execution,
				settledCall.result,
			)
			if err != nil {
				return err
			}
		} else {
			finalResult = current.scheduler.Finish(
				settledCall.execution,
				settledCall.result,
			)
		}
		commitContext := current.requestContext
		if current.requestContext.Err() != nil {
			commitContext = context.WithoutCancel(current.requestContext)
		}
		if err := current.appendToolResult(
			commitContext,
			current.calls[current.nextToCommit].block,
			finalResult,
			current.callSequences[current.nextToCommit],
		); err != nil {
			return err
		}
		for _, contextMessage := range finalResult.AdditionalContextMessages() {
			if err := current.subject.appendNextStep(contextMessage); err != nil {
				return err
			}
		}
		if err := current.batch.RecordAcceptedResult(
			current.baseIndex+current.nextToCommit,
			stopsModelContinuation(finalResult),
		); err != nil {
			return err
		}
		current.nextToCommit++
	}
	return nil
}

func (current *toolGroupExecution) appendToolCall(
	block agentmessage.ToolCallBlock,
) (int64, error) {
	draft, err := session.NewEventDraft(
		session.ToolCalled,
		session.ToolCall{
			Turn:      current.turnNumber,
			Step:      current.stepNumber,
			CallID:    block.ID,
			Name:      block.Name,
			Arguments: block.Arguments,
		},
	)
	if err != nil {
		return 0, err
	}
	commitResult, err := current.subject.commitSession(
		current.requestContext,
		session.Batch(draft),
	)
	if err != nil {
		return 0, err
	}
	return commitResult.Events[0].Seq, nil
}

func (current *toolGroupExecution) appendToolResult(
	requestContext context.Context,
	block agentmessage.ToolCallBlock,
	result tools.ToolExecutionResult,
	callSequence int64,
) error {
	payload, err := toolResultPayload(
		current.turnNumber,
		current.stepNumber,
		block,
		result,
	)
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
	_, err = current.subject.commitSession(
		requestContext,
		session.Batch(draft),
	)
	return err
}

func appendSkippedToolCall(
	subject toolGroupAgent,
	requestContext context.Context,
	turnNumber int64,
	stepNumber int64,
	block agentmessage.ToolCallBlock,
) error {
	toolReply, err := toolResultPayload(
		turnNumber,
		stepNumber,
		block,
		abortedBeforeDispatchResult(),
	)
	if err != nil {
		return err
	}
	callDraft, err := session.NewEventDraft(
		session.ToolCalled,
		session.ToolCall{
			Turn:      turnNumber,
			Step:      stepNumber,
			CallID:    block.ID,
			Name:      block.Name,
			Arguments: block.Arguments,
		},
	)
	if err != nil {
		return err
	}
	_, err = subject.commitSession(
		requestContext,
		&skippedCallPlan{
			callDraft: callDraft,
			result:    toolReply,
		},
	)
	return err
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
		return nil, fmt.Errorf(
			"agentloop: preserve invalid Tool arguments: %w",
			err,
		)
	}
	return json.RawMessage(encoded), nil
}

func toolResultPayload(
	turnNumber int64,
	stepNumber int64,
	block agentmessage.ToolCallBlock,
	result tools.ToolExecutionResult,
) (session.ToolResult, error) {
	if result == nil {
		return session.ToolResult{}, errors.New(
			"agentloop: Tool Scheduler returned a nil result",
		)
	}
	toolReply, err := agentmessage.NewToolResultMessage(
		agentmessage.ToolResultMessageInput{
			CallID:  block.ID,
			Content: result.ContentBlocks(),
			IsError: result.Failed(),
		},
	)
	if err != nil {
		return session.ToolResult{}, err
	}
	payload := session.ToolResult{
		Turn:    turnNumber,
		Step:    stepNumber,
		Message: toolReply,
		Meta:    resultMeta(result),
	}
	if failure, present := resultFailure(result); present && failure.Info != nil {
		payload.Error = &session.ToolErrorInfo{
			Name: failure.Info.Name,
			Code: failure.Info.Code,
		}
	}
	return payload, nil
}

func abortedBeforeDispatchResult() *tools.ToolExecutionFailure {
	return &tools.ToolExecutionFailure{
		Error: tools.ToolFailure{
			Message: "tool call aborted before dispatch",
			Info: &tools.ToolErrorInfo{
				Name: "AbortError",
				Code: tools.ToolAbortedBeforeDispatch,
			},
		},
		Content: []agentmessage.ContentBlock{
			agentmessage.NewTextBlock(
				"Error: tool call aborted before dispatch",
			),
		},
	}
}

// skippedCallPlan atomically creates the canonical Tool call/result pair for a
// call canceled before dispatch.
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

func resultFailure(
	result tools.ToolExecutionResult,
) (tools.ToolFailure, bool) {
	failureResult, ok := result.(*tools.ToolExecutionFailure)
	if !ok || failureResult == nil {
		return tools.ToolFailure{}, false
	}
	return failureResult.Error, true
}

func resultMeta(result tools.ToolExecutionResult) json.RawMessage {
	switch retained := result.(type) {
	case *tools.ToolExecutionSuccess:
		return append(json.RawMessage(nil), retained.Meta...)
	case *tools.ToolExecutionFailure:
		return append(json.RawMessage(nil), retained.Meta...)
	default:
		return nil
	}
}

// stopsModelContinuation maps ConcludesTurn to its AgentLoop effect: the
// accepted Tool result closes the current Turn instead of automatically
// starting another model request to consume that result.
func stopsModelContinuation(result tools.ToolExecutionResult) bool {
	succeeded, ok := result.(*tools.ToolExecutionSuccess)
	return ok && succeeded != nil && succeeded.ConcludesTurn
}
