package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/gorenx/goren/internal/jsonvalue"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

// ExecutionSubject is the minimal active-service view needed by Tool policy.
// Agent implementations satisfy it without making Tools depend on Agent's
// lifecycle, inbox, or runtime-scope contracts.
type ExecutionSubject interface {
	SessionValue() session.Context
	Inject(llm.UserMessage) error
}

// ToolExecutionToken is an opaque same-process parent correlation identity.
type ToolExecutionToken struct {
	token *toolExecutionToken
}

type toolExecutionToken struct {
	marker byte
}

// IsZero reports whether no parent execution token was supplied.
func (identity ToolExecutionToken) IsZero() bool {
	return identity.token == nil
}

// ToolExecutionInput is one caller-supplied tool call. Context cancellation is
// supplied separately to Execute and cannot be replaced in this value.
type ToolExecutionInput struct {
	CallID     llm.CallID
	RootCallID llm.CallID
	Name       string
	Arguments  json.RawMessage
	Subject    ExecutionSubject
	Parent     ToolExecutionToken
}

// ToolExecution is the immutable registry-owned identity visible to policy.
type ToolExecution struct {
	CallID     llm.CallID
	RootCallID llm.CallID
	Name       string
	Subject    ExecutionSubject
	Parent     ToolExecutionToken
	Token      ToolExecutionToken
	arguments  json.RawMessage
	state      *scheduledExecutionState
}

// ArgumentsJSON returns a detached copy of the losslessly materialized input.
func (toolCall ToolExecution) ArgumentsJSON() json.RawMessage {
	return append(json.RawMessage(nil), toolCall.arguments...)
}

// ToolRunContext is handed to the selected body after policy allows dispatch.
type ToolRunContext struct {
	Context   context.Context
	Execution ToolExecution
}

// DeferContext attaches one plugin-authored message to this toolCall's final
// result. Agent Loop admits deferred messages only after the containing tool
// result has been committed, preserving model call/result adjacency.
func (runContext ToolRunContext) DeferContext(message llm.UserMessage) {
	if runContext.Execution.state != nil {
		runContext.Execution.state.deferContext(message)
	}
}

// ConcludeTurn marks this successful execution as terminal for the current
// agent turn. Failed outcomes never carry the marker.
func (runContext ToolRunContext) ConcludeTurn() {
	if runContext.Execution.state != nil {
		runContext.Execution.state.concludeTurn()
	}
}

type scheduledExecutionState struct {
	coordinator    *executionRuntime
	requestContext context.Context
	entry          *registeredTool
	finalizer      ContentFinalizer

	mu               sync.Mutex
	phase            scheduledExecutionPhase
	bodyInvoked      bool
	concludesTurn    bool
	deferredContexts []llm.UserMessage
}

type scheduledExecutionPhase uint8

const (
	executionUnprepared scheduledExecutionPhase = iota
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
	deferredContexts []llm.UserMessage
}

func newScheduledExecutionState(
	coordinator *executionRuntime,
	requestContext context.Context,
	entry *registeredTool,
	finalizer ContentFinalizer,
) *scheduledExecutionState {
	return &scheduledExecutionState{
		coordinator:    coordinator,
		requestContext: requestContext,
		entry:          entry,
		finalizer:      finalizer,
	}
}

func (state *scheduledExecutionState) deferContext(message llm.UserMessage) {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.phase != executionDispatching {
		return
	}
	state.deferredContexts = append(state.deferredContexts, message)
}

func (state *scheduledExecutionState) concludeTurn() {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.phase != executionDispatching {
		return
	}
	state.concludesTurn = true
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
	deferred, _ := cloneUserMessages(state.deferredContexts)
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

func createExecution(input ToolExecutionInput) (ToolExecution, error) {
	identity := &toolExecutionToken{}
	rootCallID := input.RootCallID
	if rootCallID == "" {
		rootCallID = input.CallID
	}
	toolCall := ToolExecution{
		CallID:     input.CallID,
		RootCallID: rootCallID,
		Name:       input.Name,
		Subject:    input.Subject,
		Parent:     input.Parent,
		Token: ToolExecutionToken{
			token: identity,
		},
	}
	if strings.TrimSpace(input.Name) == "" ||
		input.Name != strings.TrimSpace(input.Name) {
		return toolCall, errors.New(
			"tools: tool name must be non-empty and trimmed",
		)
	}
	if input.CallID == "" {
		return toolCall, errors.New("tools: call ID is empty")
	}
	if input.Subject != nil && input.Subject.SessionValue() == nil {
		return toolCall, errors.New(
			"tools: execution subject must expose a Session",
		)
	}
	arguments, err := jsonvalue.Clone(input.Arguments)
	if err != nil {
		return toolCall, fmt.Errorf(
			"tools: arguments are not lossless JSON: %w",
			err,
		)
	}
	toolCall.arguments = arguments
	return toolCall, nil
}

func cloneExecution(source ToolExecution) ToolExecution {
	source.arguments = append(
		json.RawMessage(nil),
		source.arguments...,
	)
	return source
}

// ToolExecutionMode is the scheduler's fail-closed overlap classification.
type ToolExecutionMode string

const (
	// ExecutionParallel may overlap with sibling calls.
	ExecutionParallel ToolExecutionMode = "parallel"
	// ExecutionExclusive runs alone and forms an ordering barrier.
	ExecutionExclusive ToolExecutionMode = "exclusive"
)

// ScheduledToolStage identifies which ordered scheduler stage owns a prepared call.
type ScheduledToolStage string

const (
	// ScheduledDispatch means only the body/around-dispatch stage may overlap.
	ScheduledDispatch ScheduledToolStage = "dispatch"
	// ScheduledPostResult means the result still requires ordered post policy and finalization.
	ScheduledPostResult ScheduledToolStage = "post-result"
	// ScheduledFinalResult means policy failed terminally and only finalization remains.
	ScheduledFinalResult ScheduledToolStage = "final-result"
)

// ScheduledToolPreparation is the ordered pre-policy outcome for one call.
type ScheduledToolPreparation struct {
	Stage     ScheduledToolStage
	Execution ToolExecution
	Result    ToolExecutionResult
}

// ScheduledToolDispatch is one settled overlapping dispatch outcome.
type ScheduledToolDispatch struct {
	Result ToolExecutionResult
}

// ToolExecutionScheduler separates ordered policy/finalization from the only
// stage that Agent Loop may overlap: around-dispatch plus the tool body. An
// error is an internal scheduler failure; ordinary Tool and policy failures
// remain typed ToolExecutionResult values so Agent Loop can commit them.
type ToolExecutionScheduler interface {
	Prepare(context.Context, ToolExecutionInput) (ScheduledToolPreparation, error)
	Dispatch(ToolExecution) (ScheduledToolDispatch, error)
	Finalize(ToolExecution, ToolExecutionResult) (ToolExecutionResult, error)
	Finish(ToolExecution, ToolExecutionResult) ToolExecutionResult
}
