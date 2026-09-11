package execution

import (
	"context"
	"errors"
	"sync"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/approval"
	"github.com/gorenx/goren/tools"
)

type callExecution struct {
	tools.ToolExecution
	state *scheduledExecutionState
}

func createExecution(input tools.ToolExecutionInput) (callExecution, error) {
	view, err := tools.NewExecution(input)
	return callExecution{
		ToolExecution: view,
	}, err
}
func cloneExecution(source callExecution) tools.ToolExecution { return source.ToolExecution }

type scheduledExecutionState struct {
	coordinator    *Scheduler
	pipeline       *pipeline
	requestContext context.Context
	entry          *tools.Contract
	finalizer      tools.ContentFinalizer

	mu               sync.Mutex
	phase            scheduledExecutionPhase
	bodyInvoked      bool
	concludesTurn    bool
	deferredContexts []agentmessage.UserMessage
}

type scheduledExecutionPhase uint8

const (
	executionUnprepared scheduledExecutionPhase = iota
	executionAwaitingApproval
	executionPreparedForDispatch
	executionDispatching
	executionPreparedForPost
	executionPreparedForFinish
	executionFinalizing
	executionCompleted
)

type executionStateSnapshot struct {
	bodyInvoked      bool
	concludesTurn    bool
	deferredContexts []agentmessage.UserMessage
}

func newScheduledExecutionState(
	coordinator *Scheduler,
	requestContext context.Context,
	entry *tools.Contract,
	finalizer tools.ContentFinalizer,
) *scheduledExecutionState {
	return &scheduledExecutionState{
		coordinator:    coordinator,
		pipeline:       coordinator.pipeline,
		requestContext: requestContext,
		entry:          entry,
		finalizer:      finalizer,
	}
}

func (state *scheduledExecutionState) recordEffects(outcome tools.BodyResult) {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.phase != executionDispatching {
		return
	}
	state.deferredContexts = append(state.deferredContexts, outcome.AdditionalContexts...)
	state.concludesTurn = state.concludesTurn || outcome.ConcludesTurn
}

func (state *scheduledExecutionState) markBodyInvoked() {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.phase != executionDispatching {
		return
	}
	state.bodyInvoked = true
}

func (state *scheduledExecutionState) snapshot() executionStateSnapshot {
	state.mu.Lock()
	defer state.mu.Unlock()
	deferred := make([]agentmessage.UserMessage, len(state.deferredContexts))
	for index, message := range state.deferredContexts {
		deferred[index], _ = agentmessage.CloneUserMessage(message)
	}
	return executionStateSnapshot{
		bodyInvoked:      state.bodyInvoked,
		concludesTurn:    state.concludesTurn,
		deferredContexts: deferred,
	}
}

func (state *scheduledExecutionState) prepareForDispatch() {
	state.mu.Lock()
	state.phase = executionPreparedForDispatch
	state.mu.Unlock()
}

func (state *scheduledExecutionState) prepareForPost() {
	state.mu.Lock()
	state.phase = executionPreparedForPost
	state.mu.Unlock()
}

func (state *scheduledExecutionState) prepareForFinish() {
	state.mu.Lock()
	state.phase = executionPreparedForFinish
	state.mu.Unlock()
}

func (state *scheduledExecutionState) beginDispatch() error {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.phase != executionPreparedForDispatch {
		return errors.New("tools: execution is not prepared for dispatch")
	}
	state.phase = executionDispatching
	return nil
}

func (state *scheduledExecutionState) completeDispatch() {
	state.mu.Lock()
	if state.phase == executionDispatching {
		state.phase = executionPreparedForPost
	}
	state.mu.Unlock()
}

func (state *scheduledExecutionState) beginFinalize() error {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.phase != executionPreparedForPost {
		return errors.New(
			"tools: execution is not prepared for post-execute finalization",
		)
	}
	state.phase = executionFinalizing
	return nil
}

func (state *scheduledExecutionState) beginFinish() error {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.phase != executionPreparedForFinish {
		return errors.New(
			"tools: execution is not prepared for terminal finalization",
		)
	}
	state.phase = executionFinalizing
	return nil
}

func (state *scheduledExecutionState) completeFinalization() {
	state.mu.Lock()
	if state.phase == executionFinalizing {
		state.phase = executionCompleted
	}
	state.mu.Unlock()
}

func (state *scheduledExecutionState) awaitApproval() {
	state.mu.Lock()
	state.phase = executionAwaitingApproval
	state.mu.Unlock()
}

func (state *scheduledExecutionState) acceptApproval() error {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.phase != executionAwaitingApproval {
		return errors.New("tools: execution is not awaiting approval")
	}
	state.phase = executionUnprepared
	return nil
}

// preparation is the ordered pre-policy outcome for one call.
type preparation struct {
	Approval  *approval.Permission
	Stage     tools.ScheduledToolStage
	Execution callExecution
	Result    tools.ToolExecutionResult
}

// dispatchResult is one settled overlapping dispatch outcome.
type dispatchResult struct {
	Result tools.ToolExecutionResult
}
