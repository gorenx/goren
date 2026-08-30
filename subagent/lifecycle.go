package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
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

// OneShotOptions contains every caller-owned input for one terminal child.
type OneShotOptions struct {
	Prompt       []agentmessage.ContentBlock
	Parent       agent.Agent
	AgentOptions *agent.Options
	MaxDepth     *int64
	ToolFilter   *tools.ToolRestriction
	Persona      *string
	OutputSchema json.RawMessage
	SeedBuilder  string
	Label        *string
}

// ContinuableOptions contains every caller-owned input for one resumable
// child Session. Durable restore inputs are copied into its descriptor.
type ContinuableOptions struct {
	Prompt       []agentmessage.ContentBlock
	Parent       agent.Agent
	AgentOptions *agent.Options
	MaxDepth     *int64
	ToolFilter   *tools.ToolRestriction
	Persona      *string
	SeedBuilder  string
	Label        string
	ChildID      *session.SessionID
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
	settings OneShotOptions
}

// ContinuableStartCommand contains the validated inputs for one durable child.
type ContinuableStartCommand struct {
	settings ContinuableOptions
}

// NewOneShotStart constructs a valid OneShot command.
func NewOneShotStart(settings OneShotOptions) (OneShotStartCommand, error) {
	if err := validateSeedBuilderName(settings.SeedBuilder); err != nil {
		return OneShotStartCommand{}, err
	}
	detached, snapshotErr := snapshotOneShotOptions(settings)
	if snapshotErr != nil {
		return OneShotStartCommand{}, snapshotErr
	}
	return OneShotStartCommand{
		settings: detached,
	}, nil
}

// NewContinuableStart constructs a valid Continuable command.
func NewContinuableStart(
	settings ContinuableOptions,
) (ContinuableStartCommand, error) {
	if err := validateSeedBuilderName(settings.SeedBuilder); err != nil {
		return ContinuableStartCommand{}, err
	}
	if strings.TrimSpace(settings.Label) == "" {
		return ContinuableStartCommand{}, errors.New(
			"subagent: continuable label must be non-empty",
		)
	}
	detached, snapshotErr := snapshotContinuableOptions(settings)
	if snapshotErr != nil {
		return ContinuableStartCommand{}, snapshotErr
	}
	return ContinuableStartCommand{
		settings: detached,
	}, nil
}

// Mode selects the OneShot implementation.
func (OneShotStartCommand) Mode() Mode {
	return ModeOneShot
}

func (OneShotStartCommand) startCommand() {}

// Snapshot returns a detached copy of the validated OneShot input.
func (command OneShotStartCommand) Snapshot() (OneShotOptions, error) {
	return snapshotOneShotOptions(command.settings)
}

// SeedBuilderName returns the exact registered seed strategy name.
func (command OneShotStartCommand) SeedBuilderName() string {
	return command.settings.SeedBuilder
}

// Label returns a detached optional display label.
func (command OneShotStartCommand) Label() *string {
	return cloneOptionalString(command.settings.Label)
}

// Mode selects the Continuable implementation.
func (ContinuableStartCommand) Mode() Mode {
	return ModeContinuable
}

func (ContinuableStartCommand) startCommand() {}

// Snapshot returns a detached copy of the validated Continuable input.
func (command ContinuableStartCommand) Snapshot() (ContinuableOptions, error) {
	return snapshotContinuableOptions(command.settings)
}

// SeedBuilderName returns the exact registered seed strategy name.
func (command ContinuableStartCommand) SeedBuilderName() string {
	return command.settings.SeedBuilder
}

// Label returns the required Continuable display label.
func (command ContinuableStartCommand) Label() string {
	return command.settings.Label
}

// RequestedChildID returns the detached durable identity requested for a
// Continuable child, when present.
func (command ContinuableStartCommand) RequestedChildID() *session.SessionID {
	return cloneSessionID(command.settings.ChildID)
}

// ExecutionState is the lifecycle vocabulary shared by all Subagent modes.
// Each mode owns an independent state machine for one exact Agent lifecycle.
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
	Output     []agentmessage.ContentBlock
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

func snapshotOneShotOptions(source OneShotOptions) (OneShotOptions, error) {
	if err := validateMaxDepth(source.MaxDepth); err != nil {
		return OneShotOptions{}, err
	}
	promptSnapshot, cloneErr := agentmessage.CloneContentBlocks(source.Prompt)
	if cloneErr != nil {
		return OneShotOptions{}, cloneErr
	}
	filterSnapshot, snapshotErr := snapshotToolRestriction(source.ToolFilter)
	if snapshotErr != nil {
		return OneShotOptions{}, snapshotErr
	}
	return OneShotOptions{
		Prompt:       promptSnapshot,
		Parent:       source.Parent,
		AgentOptions: snapshotAgentOptions(source.AgentOptions),
		MaxDepth:     cloneInt64(source.MaxDepth),
		ToolFilter:   filterSnapshot,
		Persona:      cloneOptionalString(source.Persona),
		OutputSchema: append(json.RawMessage(nil), source.OutputSchema...),
		SeedBuilder:  source.SeedBuilder,
		Label:        cloneOptionalString(source.Label),
	}, nil
}

func snapshotContinuableOptions(
	source ContinuableOptions,
) (ContinuableOptions, error) {
	if err := validateMaxDepth(source.MaxDepth); err != nil {
		return ContinuableOptions{}, err
	}
	promptSnapshot, cloneErr := agentmessage.CloneContentBlocks(source.Prompt)
	if cloneErr != nil {
		return ContinuableOptions{}, cloneErr
	}
	filterSnapshot, snapshotErr := snapshotToolRestriction(source.ToolFilter)
	if snapshotErr != nil {
		return ContinuableOptions{}, snapshotErr
	}
	return ContinuableOptions{
		Prompt:       promptSnapshot,
		Parent:       source.Parent,
		AgentOptions: snapshotAgentOptions(source.AgentOptions),
		MaxDepth:     cloneInt64(source.MaxDepth),
		ToolFilter:   filterSnapshot,
		Persona:      cloneOptionalString(source.Persona),
		SeedBuilder:  source.SeedBuilder,
		Label:        source.Label,
		ChildID:      cloneSessionID(source.ChildID),
	}, nil
}

func validateMaxDepth(maxDepth *int64) error {
	if maxDepth != nil &&
		(*maxDepth < 0 || *maxDepth > maxSafeInteger) {
		return errors.New(
			"subagent: maxDepth must be a non-negative safe integer",
		)
	}
	return nil
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
