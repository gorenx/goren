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

// Mode distinguishes the two Subagent implementations in durable descriptors.
type Mode string

const (
	// ModeOneShot is one disposable foreground delegation with one result.
	ModeOneShot Mode = "one-shot"
	// ModeContinuable is one durable child conversation with resumable turns.
	ModeContinuable Mode = "continuable"
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

// StartCommand is a validated closed command for starting either Subagent
// implementation. Private fields prevent mixed or incomplete variants.
type StartCommand struct {
	mode        Mode
	request     ChildRequest
	seedBuilder string
	label       *string
	childID     *session.SessionID
}

// NewOneShotStart constructs a valid OneShot command.
func NewOneShotStart(
	input ChildRequest,
	options OneShotOptions,
) (StartCommand, error) {
	if err := validateSeedBuilderName(options.SeedBuilder); err != nil {
		return StartCommand{}, err
	}
	return StartCommand{
		mode:        ModeOneShot,
		request:     input,
		seedBuilder: options.SeedBuilder,
		label:       cloneOptionalString(options.Label),
	}, nil
}

// NewContinuableStart constructs a valid Continuable command.
func NewContinuableStart(
	input ChildRequest,
	options ContinuableOptions,
) (StartCommand, error) {
	if err := validateSeedBuilderName(options.SeedBuilder); err != nil {
		return StartCommand{}, err
	}
	if strings.TrimSpace(options.Label) == "" {
		return StartCommand{}, errors.New(
			"subagent: continuable label must be non-empty",
		)
	}
	labelValue := options.Label
	return StartCommand{
		mode:        ModeContinuable,
		request:     input,
		seedBuilder: options.SeedBuilder,
		label:       &labelValue,
		childID:     cloneSessionID(options.ChildID),
	}, nil
}

// Mode returns the selected Subagent implementation.
func (command StartCommand) Mode() Mode {
	return command.mode
}

// Request returns the caller input. Starter snapshots it before asynchronous
// work begins.
func (command StartCommand) Request() ChildRequest {
	return command.request
}

// SeedBuilderName returns the exact registered seed strategy name.
func (command StartCommand) SeedBuilderName() string {
	return command.seedBuilder
}

// Label returns a detached optional display label.
func (command StartCommand) Label() *string {
	return cloneOptionalString(command.label)
}

// RequestedChildID returns the detached durable identity requested for a
// Continuable child, when present.
func (command StartCommand) RequestedChildID() *session.SessionID {
	return cloneSessionID(command.childID)
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

// Execution observes and controls one exact Subagent execution. Dispose ends
// only this execution; it does not delete a Continuable child Session.
type Execution interface {
	RunID() RunID
	ChildID() session.SessionID
	State() ExecutionState
	AwaitTerminal(context.Context) (Terminal, error)
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
