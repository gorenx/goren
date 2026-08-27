package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/tools"
)

// Mode distinguishes Subagent implementations in durable descriptors.
type Mode string

const (
	// ModeOneShot is one disposable foreground delegation with one result.
	ModeOneShot Mode = "one-shot"
	// ModeContinuable is one durable child conversation with resumable turns.
	ModeContinuable Mode = "continuable"
	// ModeBound is one child whose initial start is driven by a durable parent
	// binding and whose runtime configuration comes from the Bound owner.
	ModeBound Mode = "bound"
)

// ChildRequest contains caller-owned inputs shared by both implementations.
// The selected implementation snapshots and validates it before Agent creation.
type ChildRequest struct {
	Prompt       []llm.ContentBlock
	Parent       agent.Agent
	AgentOptions *agent.Options
	MaxDepth     *int64
	ToolFilter   *tools.ToolRestriction
	Persona      *string
	OutputSchema json.RawMessage
}

// OneShotOptions contains inputs unique to one terminal child execution.
type OneShotOptions struct {
	SeedBuilder string
	Label       *string
}

// ContinuableOptions contains inputs unique to one resumable child Session.
type ContinuableOptions struct {
	SeedBuilder string
	Label       string
	ChildID     *session.SessionID
}

// StartCommand is the closed family of mode-owned start commands. The private
// marker prevents callers from adding variants while each concrete command
// keeps only the inputs required by its mode.
type StartCommand interface {
	Mode() Mode
	startCommand()
}

// OneShotStartCommand contains the validated inputs for one terminal child.
type OneShotStartCommand struct {
	request     ChildRequest
	seedBuilder string
	label       *string
}

// ContinuableStartCommand contains the validated inputs for one durable child.
type ContinuableStartCommand struct {
	request     ChildRequest
	seedBuilder string
	label       string
	childID     *session.SessionID
}

// BoundStartCommand identifies one already-bound child to initialize for an
// exact live parent. Bound-owned creation input and configuration are loaded by
// the Bound implementation rather than copied into this command.
type BoundStartCommand struct {
	parent  agent.Agent
	childID session.SessionID
}

// NewOneShotStart constructs a valid OneShot command.
func NewOneShotStart(
	input ChildRequest,
	options OneShotOptions,
) (OneShotStartCommand, error) {
	if err := validateSeedBuilderName(options.SeedBuilder); err != nil {
		return OneShotStartCommand{}, err
	}
	requestSnapshot, snapshotErr := snapshotChildRequest(input)
	if snapshotErr != nil {
		return OneShotStartCommand{}, snapshotErr
	}
	return OneShotStartCommand{
		request:     requestSnapshot,
		seedBuilder: options.SeedBuilder,
		label:       cloneOptionalString(options.Label),
	}, nil
}

// NewContinuableStart constructs a valid Continuable command.
func NewContinuableStart(
	input ChildRequest,
	options ContinuableOptions,
) (ContinuableStartCommand, error) {
	if err := validateSeedBuilderName(options.SeedBuilder); err != nil {
		return ContinuableStartCommand{}, err
	}
	if strings.TrimSpace(options.Label) == "" {
		return ContinuableStartCommand{}, errors.New(
			"subagent: continuable label must be non-empty",
		)
	}
	requestSnapshot, snapshotErr := snapshotChildRequest(input)
	if snapshotErr != nil {
		return ContinuableStartCommand{}, snapshotErr
	}
	return ContinuableStartCommand{
		request:     requestSnapshot,
		seedBuilder: options.SeedBuilder,
		label:       options.Label,
		childID:     cloneSessionID(options.ChildID),
	}, nil
}

// NewBoundStart constructs a command for one binding-selected child. The Bound
// implementation must still verify that the durable binding exists.
func NewBoundStart(
	parentAgent agent.Agent,
	boundChildID session.SessionID,
) (BoundStartCommand, error) {
	if parentAgent == nil {
		return BoundStartCommand{}, errors.New(
			"subagent: Bound start parent Agent is nil",
		)
	}
	if strings.TrimSpace(string(boundChildID)) == "" ||
		string(boundChildID) != strings.TrimSpace(string(boundChildID)) {
		return BoundStartCommand{}, errors.New(
			"subagent: Bound start child Session ID must be non-empty and trimmed",
		)
	}
	return BoundStartCommand{
		parent:  parentAgent,
		childID: boundChildID,
	}, nil
}

// Mode selects the OneShot implementation.
func (OneShotStartCommand) Mode() Mode {
	return ModeOneShot
}

func (OneShotStartCommand) startCommand() {}

// Request returns a detached copy of the validated OneShot input.
func (command OneShotStartCommand) Request() (ChildRequest, error) {
	return snapshotChildRequest(command.request)
}

// SeedBuilderName returns the exact registered seed strategy name.
func (command OneShotStartCommand) SeedBuilderName() string {
	return command.seedBuilder
}

// Label returns a detached optional display label.
func (command OneShotStartCommand) Label() *string {
	return cloneOptionalString(command.label)
}

// Mode selects the Continuable implementation.
func (ContinuableStartCommand) Mode() Mode {
	return ModeContinuable
}

func (ContinuableStartCommand) startCommand() {}

// Request returns a detached copy of the validated Continuable input.
func (command ContinuableStartCommand) Request() (ChildRequest, error) {
	return snapshotChildRequest(command.request)
}

// SeedBuilderName returns the exact registered seed strategy name.
func (command ContinuableStartCommand) SeedBuilderName() string {
	return command.seedBuilder
}

// Label returns the required Continuable display label.
func (command ContinuableStartCommand) Label() string {
	return command.label
}

// RequestedChildID returns the detached durable identity requested for a
// Continuable child, when present.
func (command ContinuableStartCommand) RequestedChildID() *session.SessionID {
	return cloneSessionID(command.childID)
}

// Mode selects the Bound implementation.
func (BoundStartCommand) Mode() Mode {
	return ModeBound
}

func (BoundStartCommand) startCommand() {}

// Parent returns the exact live parent that discovered the committed binding.
func (command BoundStartCommand) Parent() agent.Agent {
	return command.parent
}

// ChildID returns the durable child identity stored by the binding.
func (command BoundStartCommand) ChildID() session.SessionID {
	return command.childID
}

// ExecutionState is the single lifecycle vocabulary used by both
// implementations for one exact Agent epoch.
type ExecutionState string

const (
	// ExecutionStarting has not crossed initial Inbox acceptance.
	ExecutionStarting ExecutionState = "starting"
	// ExecutionActive is published and accepts mode-appropriate commands.
	ExecutionActive ExecutionState = "active"
	// ExecutionStopping has one claimed terminal transaction.
	ExecutionStopping ExecutionState = "stopping"
	// ExecutionStopped has memoized its terminal result.
	ExecutionStopped ExecutionState = "stopped"
)

// Terminal is the immutable outcome of one exact Subagent Execution.
type Terminal struct {
	Output     []llm.ContentBlock
	Structured json.RawMessage
	Diagnostic *string
	StopReason StopReason
}

// StopReason is the caller-visible outcome, distinct from the internal trigger
// that claimed teardown.
type StopReason string

const (
	// StopCompleted means the child finished normally.
	StopCompleted StopReason = "completed"
	// StopAborted means cancellation or disposal stopped the child.
	StopAborted StopReason = "aborted"
	// StopError means a model, transport, or settlement failure occurred.
	StopError StopReason = "error"
	// StopMaxTokens means the child exhausted its token ceiling.
	StopMaxTokens StopReason = "max-tokens"
	// StopRefusal means the child declined the task.
	StopRefusal StopReason = "refusal"
)

// Execution observes and controls one exact Subagent execution. Wait only
// waits for termination; Result only reads an already stored outcome. Dispose
// ends only this execution and does not delete a Continuable child Session.
type Execution interface {
	RunID() RunID
	ChildID() session.SessionID
	State() ExecutionState
	Wait(context.Context) error
	Result() (Terminal, bool)
	Dispose(context.Context) error
}

// Starter starts either implementation from a validated command.
type Starter interface {
	Start(context.Context, StartCommand) (Execution, error)
}

func validateSeedBuilderName(builderName string) error {
	if strings.TrimSpace(builderName) == "" ||
		builderName != strings.TrimSpace(builderName) {
		return errors.New(
			"subagent: SeedBuilder name must be non-empty and trimmed",
		)
	}
	return nil
}

func cloneOptionalString(source *string) *string {
	if source == nil {
		return nil
	}
	detached := *source
	return &detached
}

func cloneSessionID(source *session.SessionID) *session.SessionID {
	if source == nil {
		return nil
	}
	detached := *source
	return &detached
}

const maxSafeInteger int64 = 1<<53 - 1

func snapshotChildRequest(source ChildRequest) (ChildRequest, error) {
	if source.MaxDepth != nil &&
		(*source.MaxDepth < 0 || *source.MaxDepth > maxSafeInteger) {
		return ChildRequest{}, errors.New(
			"subagent: maxDepth must be a non-negative safe integer",
		)
	}
	promptSnapshot, cloneErr := llm.CloneContentBlocks(source.Prompt)
	if cloneErr != nil {
		return ChildRequest{}, cloneErr
	}
	filterSnapshot, snapshotErr := snapshotToolRestriction(source.ToolFilter)
	if snapshotErr != nil {
		return ChildRequest{}, snapshotErr
	}
	return ChildRequest{
		Prompt:       promptSnapshot,
		Parent:       source.Parent,
		AgentOptions: snapshotAgentOptions(source.AgentOptions),
		MaxDepth:     cloneInt64(source.MaxDepth),
		ToolFilter:   filterSnapshot,
		Persona:      cloneOptionalString(source.Persona),
		OutputSchema: append(json.RawMessage(nil), source.OutputSchema...),
	}, nil
}

func snapshotAgentOptions(source *agent.Options) *agent.Options {
	if source == nil {
		return nil
	}
	detached := *source
	if source.MaxTokens != nil {
		maxTokensValue := *source.MaxTokens
		detached.MaxTokens = &maxTokensValue
	}
	return &detached
}

func snapshotToolRestriction(
	filterValue *tools.ToolRestriction,
) (*tools.ToolRestriction, error) {
	if filterValue == nil {
		return nil, nil
	}
	if filterValue.Allow == nil && filterValue.Deny == nil {
		return nil, errors.New(
			"subagent: toolFilter must declare allow and/or deny",
		)
	}
	return &tools.ToolRestriction{
		Allow: cloneStrings(filterValue.Allow),
		Deny:  cloneStrings(filterValue.Deny),
	}, nil
}

func cloneInt64(source *int64) *int64 {
	if source == nil {
		return nil
	}
	detached := *source
	return &detached
}
