// Package tools owns tool definitions, scope-aware visibility, execution
// policy, canonical output validation, and model-facing schema projection.
package tools

import (
	"context"
	"encoding/json"
	"time"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
)

const (
	// ServiceName is the canonical Cordis service name.
	ServiceName = "tools"
	// PreExecuteEventName is the canonical pre-dispatch policy waterfall.
	PreExecuteEventName = "tools/pre-execute"
	// ExecuteEventName is the canonical around-dispatch waterfall.
	ExecuteEventName = "tools/execute"
	// PostExecuteEventName is the canonical result-policy waterfall.
	PostExecuteEventName = "tools/post-execute"
	// ResultEventName is the canonical final-outcome notification.
	ResultEventName = "tools/result"
	// ChangeEventName is the unfiltered registry-change notification.
	ChangeEventName = "tools/change"
	// ToolAborted is the stable code for cancellation after body invocation.
	ToolAborted = "ABORTED"
	// ToolAbortedBeforeDispatch is the stable code for cancellation before body invocation.
	ToolAbortedBeforeDispatch = "ABORTED_BEFORE_DISPATCH"
	// RunCodeName is reserved for the future Code Mode presentation transport.
	RunCodeName = "run_code"
)

// Executor runs one already-snapshotted tool call and returns a canonical JSON
// value. Implementations must observe ToolRunContext.Context and settle owned
// work before returning.
type Executor interface {
	Execute(json.RawMessage, ToolRunContext) (json.RawMessage, error)
}

// ExecutorFunc adapts a function to Executor.
type ExecutorFunc func(json.RawMessage, ToolRunContext) (json.RawMessage, error)

// Execute invokes the adapted function.
func (operation ExecutorFunc) Execute(arguments json.RawMessage, runContext ToolRunContext) (json.RawMessage, error) {
	return operation(arguments, runContext)
}

// OutputRenderer projects validated arguments and a validated canonical value
// into model-facing content.
type OutputRenderer interface {
	Render(json.RawMessage, json.RawMessage) ([]llm.ContentBlock, error)
}

// OutputRendererFunc adapts a function to OutputRenderer.
type OutputRendererFunc func(json.RawMessage, json.RawMessage) ([]llm.ContentBlock, error)

// Render invokes the adapted function.
func (operation OutputRendererFunc) Render(arguments json.RawMessage, value json.RawMessage) ([]llm.ContentBlock, error) {
	return operation(arguments, value)
}

// PresentationProjector derives replayable tool-private UI metadata from one
// top-level successful value.
type PresentationProjector interface {
	Project(json.RawMessage, json.RawMessage) (json.RawMessage, error)
}

// PresentationProjectorFunc adapts a function to PresentationProjector.
type PresentationProjectorFunc func(json.RawMessage, json.RawMessage) (json.RawMessage, error)

// Project invokes the adapted function.
func (operation PresentationProjectorFunc) Project(arguments json.RawMessage, value json.RawMessage) (json.RawMessage, error) {
	return operation(arguments, value)
}

// ContentFinalizer performs the snapshotted definition-owned last-mile
// content transform. replace=false preserves the supplied content.
type ContentFinalizer interface {
	Finalize(ToolExecution, ToolResultSnapshot) (content []llm.ContentBlock, replace bool)
}

// ContentFinalizerFunc adapts a function to ContentFinalizer.
type ContentFinalizerFunc func(ToolExecution, ToolResultSnapshot) ([]llm.ContentBlock, bool)

// Finalize invokes the adapted function.
func (operation ContentFinalizerFunc) Finalize(execution ToolExecution, snapshot ToolResultSnapshot) ([]llm.ContentBlock, bool) {
	return operation(execution, snapshot)
}

// ConcurrencyClassifier opts a call into overlap with sibling tool calls.
// False, absence, or panic is fail-closed exclusive scheduling.
type ConcurrencyClassifier interface {
	ConcurrencySafe(json.RawMessage) bool
}

// ConcurrencyClassifierFunc adapts a function to ConcurrencyClassifier.
type ConcurrencyClassifierFunc func(json.RawMessage) bool

// ConcurrencySafe invokes the adapted function.
func (operation ConcurrencyClassifierFunc) ConcurrencySafe(arguments json.RawMessage) bool {
	return operation(arguments)
}

// ToolOutputDefinition owns the successful value schema and its projections.
type ToolOutputDefinition struct {
	Schema           json.RawMessage
	Renderer         OutputRenderer
	PresentationMeta PresentationProjector
}

// ToolDefinition combines the model-facing schema with host-only behavior.
type ToolDefinition struct {
	Name                string
	Description         string
	Parameters          json.RawMessage
	Output              ToolOutputDefinition
	Executor            Executor
	FinalizeContent     ContentFinalizer
	Timeout             time.Duration
	ConcurrencyBehavior ConcurrencyClassifier
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
	Scope      plugin.ScopeKey
	Parent     ToolExecutionToken
}

// ToolExecution is the immutable registry-owned identity visible to policy.
type ToolExecution struct {
	CallID     llm.CallID
	RootCallID llm.CallID
	Name       string
	Scope      plugin.ScopeKey
	Parent     ToolExecutionToken
	Token      ToolExecutionToken
	arguments  json.RawMessage
}

// ArgumentsJSON returns a detached copy of the losslessly materialized input.
func (execution ToolExecution) ArgumentsJSON() json.RawMessage {
	return append(json.RawMessage(nil), execution.arguments...)
}

// ToolRunContext is handed to the selected body after policy allows dispatch.
type ToolRunContext struct {
	Context   context.Context
	Execution ToolExecution
	conclude  func()
}

// ConcludeTurn marks this successful execution as terminal for the current
// agent turn. Failed outcomes never carry the marker.
func (runContext ToolRunContext) ConcludeTurn() {
	if runContext.conclude != nil {
		runContext.conclude()
	}
}

// ToolExecutionMode is the scheduler's fail-closed overlap classification.
type ToolExecutionMode string

const (
	// ExecutionParallel may overlap with sibling calls.
	ExecutionParallel ToolExecutionMode = "parallel"
	// ExecutionExclusive runs alone and forms an ordering barrier.
	ExecutionExclusive ToolExecutionMode = "exclusive"
)

// ToolErrorInfo is stable internal routing metadata for a failed call.
type ToolErrorInfo struct {
	Name string `json:"name"`
	Code string `json:"code"`
}

// ToolFailure is the canonical structured failure detail.
type ToolFailure struct {
	Message string         `json:"message"`
	Info    *ToolErrorInfo `json:"info,omitempty"`
}

// ToolExecutionResult is the closed success/failure outcome union.
type ToolExecutionResult interface {
	Failed() bool
	ContentBlocks() []llm.ContentBlock
	cloneResult() (ToolExecutionResult, error)
}

// ToolResultSnapshot is the read-only observer view of one final outcome.
// Every accessor that exposes reference-backed data returns a detached copy.
type ToolResultSnapshot interface {
	Failed() bool
	ContentBlocks() []llm.ContentBlock
	SuccessValue() (json.RawMessage, bool)
	FailureDetail() (ToolFailure, bool)
	PresentationMeta() json.RawMessage
	ConcludesAgentTurn() bool
}

// ToolExecutionSuccess is a validated canonical value and its projections.
type ToolExecutionSuccess struct {
	Value         json.RawMessage
	Content       []llm.ContentBlock
	Meta          json.RawMessage
	ConcludesTurn bool
	owner         *toolExecutionToken
}

// Failed reports false.
func (*ToolExecutionSuccess) Failed() bool { return false }

// ContentBlocks returns the result's model-facing content.
func (outcome *ToolExecutionSuccess) ContentBlocks() []llm.ContentBlock {
	if outcome == nil {
		return nil
	}
	detached, _ := llm.CloneContentBlocks(outcome.Content)
	return detached
}

// ToolExecutionFailure is a normalized error and its model-facing content.
type ToolExecutionFailure struct {
	Error   ToolFailure
	Content []llm.ContentBlock
	Meta    json.RawMessage
}

// Failed reports true.
func (*ToolExecutionFailure) Failed() bool { return true }

// ContentBlocks returns the result's model-facing content.
func (outcome *ToolExecutionFailure) ContentBlocks() []llm.ContentBlock {
	if outcome == nil {
		return nil
	}
	detached, _ := llm.CloneContentBlocks(outcome.Content)
	return detached
}

// PreToolDecision is the closed pre-dispatch policy union.
type PreToolDecision interface {
	preToolDecision()
}

// AllowDecision permits dispatch.
type AllowDecision struct{}

func (AllowDecision) preToolDecision() {}

// DenyDecision rejects dispatch with model-visible feedback.
type DenyDecision struct{ Reason string }

func (DenyDecision) preToolDecision() {}

// AskDecision requires approval; without an approval provider it degrades to denial.
type AskDecision struct{ Reason string }

func (AskDecision) preToolDecision() {}

// PostToolDecision is the closed post-dispatch policy union.
type PostToolDecision interface {
	postToolDecision()
}

// AcceptDecision preserves the normalized result.
type AcceptDecision struct{}

func (AcceptDecision) postToolDecision() {}

// ReplaceContentDecision replaces only the model-facing projection.
type ReplaceContentDecision struct{ Content []llm.ContentBlock }

func (ReplaceContentDecision) postToolDecision() {}

// ReplaceValueDecision replaces a successful canonical value and re-renders it.
type ReplaceValueDecision struct{ Value json.RawMessage }

func (ReplaceValueDecision) postToolDecision() {}

// BlockDecision turns corrective feedback into a failed result.
type BlockDecision struct{ Feedback []llm.ContentBlock }

func (BlockDecision) postToolDecision() {}

// ToolRestriction intersects inherited capability visibility. A nil field is
// absent; a non-nil empty Allow intentionally admits no inherited tools.
type ToolRestriction struct {
	Allow []string
	Deny  []string
}

// ToolGuard is a monotonic synchronous denial policy.
type ToolGuard interface {
	DenyReason(ToolExecution) (string, bool)
}

// ToolGuardFunc adapts a function to ToolGuard.
type ToolGuardFunc func(ToolExecution) (string, bool)

// DenyReason invokes the adapted function.
func (operation ToolGuardFunc) DenyReason(execution ToolExecution) (string, bool) {
	return operation(execution)
}

// ResultObserverReporter contains tools/result listener failures without
// changing the already-final outcome.
type ResultObserverReporter interface {
	ReportToolObserverError(context.Context, ToolExecution, error)
}

// ToolRuntime is the provider-owned registry and execution capability.
type ToolRuntime interface {
	Register(context.Context, *plugin.Scope, ToolDefinition) (plugin.Disposer, error)
	Restrict(context.Context, *plugin.Scope, ToolRestriction) (plugin.Disposer, error)
	Guard(*plugin.Scope, ToolGuard) (plugin.Disposer, error)
	Get(string, plugin.ScopeKey) (ToolDefinition, bool)
	Schemas(plugin.ScopeKey) []llm.ToolSchema
	ExecutionMode(ToolExecutionInput) ToolExecutionMode
	Execute(context.Context, ToolExecutionInput) ToolExecutionResult
}

// Service is the canonical Tools service definition.
var Service = plugin.DefineService[ToolRuntime](ServiceName)
