package subagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/tools"
)

const (
	// BoundBindingEventName records one immutable parent-to-child binding and
	// the inputs required to create that child exactly once.
	BoundBindingEventName = "subagent/bound-binding"
	// BoundConfigEventName records one complete revision of mutable Bound
	// child configuration in the exact parent Session.
	BoundConfigEventName = "subagent/bound-config"
	// BoundConfigAppliedEventName records which parent config revision was
	// installed before later work entered the child Session.
	BoundConfigAppliedEventName = "subagent/bound-config-applied"
	// BoundMaterializationEventName records the result of one Bound child
	// create or restore attempt separately from its mutable configuration.
	BoundMaterializationEventName = "subagent/bound-materialization"
	// BoundCursorEventName records the half-open parent Session prefix already
	// skipped or durably admitted to one Bound child Inbox.
	BoundCursorEventName = "subagent/bound-cursor"
	// BoundEventVersion is the first complete persisted Bound contract.
	BoundEventVersion = 1
)

// BoundConfigInput is one caller-owned complete mutable configuration.
type BoundConfigInput struct {
	Enabled         bool
	Persona         *string
	ToolRestriction *tools.ToolRestriction
	Extensions      []string
}

// BindCommand establishes one durable binding before child creation begins.
type BindCommand struct {
	Parent           agent.Agent
	RequestedChildID *session.SessionID
	SeedBuilder      string
	Title            string
	InitialPrompt    []agentmessage.ContentBlock
	AgentOptions     *agent.Options
	MaxDepth         *int64
	Config           BoundConfigInput
}

// BoundBinding identifies one binding committed in the exact parent Session.
type BoundBinding struct {
	ParentSessionID session.SessionID
	ChildSessionID  session.SessionID
	ConfigRevision  int64
}

// UpdateBoundConfigCommand replaces one complete config at an expected
// revision. Parent must be the exact live parent Agent.
type UpdateBoundConfigCommand struct {
	Parent           agent.Agent
	ChildSessionID   session.SessionID
	ExpectedRevision int64
	Config           BoundConfigInput
}

// UpdateBoundConfigResult identifies the committed complete revision.
type UpdateBoundConfigResult struct {
	ParentSessionID session.SessionID
	ChildSessionID  session.SessionID
	Revision        int64
}

// BoundRegistry establishes Bound children and updates their mutable config.
// Binding/config durability belongs to the exact parent Session.
type BoundRegistry interface {
	Bind(context.Context, BindCommand) (BoundBinding, error)
	UpdateConfig(
		context.Context,
		UpdateBoundConfigCommand,
	) (UpdateBoundConfigResult, error)
}

// BoundCreation is the immutable input persisted with one binding.
type BoundCreation struct {
	SeedBuilder   string
	Title         string
	InitialPrompt []agentmessage.ContentBlock
	AgentOptions  agent.Options
}

// BoundBindingData is the owner-defined binding event payload.
type BoundBindingData struct {
	Version        int               `json:"version"`
	ChildSessionID session.SessionID `json:"childSessionId"`
	Creation       BoundCreation     `json:"creation"`
}

// BoundConfigSnapshot is one detached complete persisted revision.
type BoundConfigSnapshot struct {
	Enabled         bool
	Persona         *string
	ToolRestriction *tools.ToolRestriction
	Extensions      []string
}

// BoundConfigData is the owner-defined config event payload.
type BoundConfigData struct {
	Version          int                 `json:"version"`
	ChildSessionID   session.SessionID   `json:"childSessionId"`
	PreviousRevision int64               `json:"previousRevision"`
	Revision         int64               `json:"revision"`
	Config           BoundConfigSnapshot `json:"config"`
}

// BoundConfigAppliedData correlates one child with its durable parent config.
type BoundConfigAppliedData struct {
	Version              int               `json:"version"`
	ParentSessionID      session.SessionID `json:"parentSessionId"`
	ParentConfigEventSeq int64             `json:"parentConfigEventSeq"`
	Revision             int64             `json:"revision"`
}

// BoundMaterializationResult classifies one durable child create/restore
// attempt without copying configuration or diagnostics into the event.
type BoundMaterializationResult string

const (
	BoundMaterializationSucceeded BoundMaterializationResult = "succeeded"
	BoundMaterializationFailed    BoundMaterializationResult = "failed"
)

// BoundMaterializationData is kept separate from binding and config facts.
type BoundMaterializationData struct {
	Version        int                        `json:"version"`
	ChildSessionID session.SessionID          `json:"childSessionId"`
	ConfigRevision int64                      `json:"configRevision"`
	Result         BoundMaterializationResult `json:"result"`
}

// BoundCursorDisposition classifies why one parent prefix may advance.
type BoundCursorDisposition string

const (
	// BoundCursorDelivered means the exact interaction receipt is durable
	// in the child Session before this parent progress fact commits.
	BoundCursorDelivered BoundCursorDisposition = "delivered"
	// BoundCursorSkipped means the completed parent turn contained no
	// direct user interaction and therefore produced no child message.
	BoundCursorSkipped BoundCursorDisposition = "skipped"
)

// BoundCursor is one monotonic per-binding parent interaction cursor. NextSeq
// is half-open: every parent event before it was handled.
type BoundCursor struct {
	Version         int                    `json:"version"`
	ChildSessionID  session.SessionID      `json:"childSessionId"`
	PreviousNextSeq int64                  `json:"previousNextSeq"`
	NextSeq         int64                  `json:"nextSeq"`
	ThroughTurn     int64                  `json:"throughTurn"`
	Disposition     BoundCursorDisposition `json:"disposition"`
}

func (source BoundCreation) MarshalJSON() ([]byte, error) {
	if err := validateBoundCreation(
		source.SeedBuilder,
		source.Title,
		source.InitialPrompt,
	); err != nil {
		return nil, err
	}
	if err := validateBoundAgentOptions(source.AgentOptions); err != nil {
		return nil, err
	}
	prompt, err := agentmessage.CloneContentBlocks(source.InitialPrompt)
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		SeedBuilder   string                      `json:"seedBuilder"`
		Title         string                      `json:"title"`
		InitialPrompt []agentmessage.ContentBlock `json:"initialPrompt"`
		AgentProvider string                      `json:"agentProvider"`
		AgentModel    string                      `json:"agentModel"`
		MaxTokens     *int                        `json:"maxTokens,omitempty"`
	}{
		SeedBuilder:   source.SeedBuilder,
		Title:         source.Title,
		InitialPrompt: prompt,
		AgentProvider: source.AgentOptions.Provider,
		AgentModel:    source.AgentOptions.Model,
		MaxTokens:     cloneInt(source.AgentOptions.MaxTokens),
	})
}

func (target *BoundCreation) UnmarshalJSON(rawValue []byte) error {
	var wireValue struct {
		SeedBuilder   string          `json:"seedBuilder"`
		Title         string          `json:"title"`
		InitialPrompt json.RawMessage `json:"initialPrompt"`
		AgentProvider string          `json:"agentProvider"`
		AgentModel    string          `json:"agentModel"`
		MaxTokens     *int            `json:"maxTokens,omitempty"`
	}
	if err := decodeBoundJSON(rawValue, &wireValue); err != nil {
		return err
	}
	prompt, err := agentmessage.DecodeContentBlocks(wireValue.InitialPrompt)
	if err != nil {
		return err
	}
	if err = validateBoundCreation(
		wireValue.SeedBuilder,
		wireValue.Title,
		prompt,
	); err != nil {
		return err
	}
	if err = validateBoundAgentOptions(
		agent.Options{
			Provider:  wireValue.AgentProvider,
			Model:     wireValue.AgentModel,
			MaxTokens: wireValue.MaxTokens,
		},
	); err != nil {
		return err
	}
	*target = BoundCreation{
		SeedBuilder:   wireValue.SeedBuilder,
		Title:         wireValue.Title,
		InitialPrompt: prompt,
		AgentOptions: agent.Options{
			Provider:  wireValue.AgentProvider,
			Model:     wireValue.AgentModel,
			MaxTokens: cloneInt(wireValue.MaxTokens),
		},
	}
	return nil
}

func (source BoundConfigSnapshot) MarshalJSON() ([]byte, error) {
	restriction, err := encodeToolRestriction(source.ToolRestriction)
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Enabled         bool                   `json:"enabled"`
		Persona         *string                `json:"persona,omitempty"`
		ToolRestriction *toolRestrictionRecord `json:"toolRestriction,omitempty"`
		Extensions      []string               `json:"extensions"`
	}{
		Enabled:         source.Enabled,
		Persona:         cloneString(source.Persona),
		ToolRestriction: restriction,
		Extensions:      cloneStrings(source.Extensions),
	})
}

func (target *BoundConfigSnapshot) UnmarshalJSON(rawValue []byte) error {
	var wireValue struct {
		Enabled         bool                   `json:"enabled"`
		Persona         *string                `json:"persona,omitempty"`
		ToolRestriction *toolRestrictionRecord `json:"toolRestriction,omitempty"`
		Extensions      []string               `json:"extensions"`
	}
	if err := decodeBoundJSON(rawValue, &wireValue); err != nil {
		return err
	}
	var restriction *tools.ToolRestriction
	if wireValue.ToolRestriction != nil {
		if wireValue.ToolRestriction.Allow == nil &&
			wireValue.ToolRestriction.Deny == nil {
			return errors.New(
				"subagent: Bound toolRestriction must declare allow and/or deny",
			)
		}
		restriction = &tools.ToolRestriction{}
		if wireValue.ToolRestriction.Allow != nil {
			restriction.Allow = cloneStrings(*wireValue.ToolRestriction.Allow)
		}
		if wireValue.ToolRestriction.Deny != nil {
			restriction.Deny = cloneStrings(*wireValue.ToolRestriction.Deny)
		}
	}
	if err := validateExtensionNames(wireValue.Extensions); err != nil {
		return err
	}
	*target = BoundConfigSnapshot{
		Enabled:         wireValue.Enabled,
		Persona:         cloneString(wireValue.Persona),
		ToolRestriction: restriction,
		Extensions:      cloneStrings(wireValue.Extensions),
	}
	return nil
}

func SnapshotBoundConfig(source BoundConfigInput) (BoundConfigSnapshot, error) {
	restriction, err := cloneToolRestriction(source.ToolRestriction)
	if err != nil {
		return BoundConfigSnapshot{}, err
	}
	if err = validateExtensionNames(source.Extensions); err != nil {
		return BoundConfigSnapshot{}, err
	}
	return BoundConfigSnapshot{
		Enabled:         source.Enabled,
		Persona:         cloneString(source.Persona),
		ToolRestriction: restriction,
		Extensions:      cloneStrings(source.Extensions),
	}, nil
}

func validateBoundCreation(
	builderName string,
	title string,
	prompt []agentmessage.ContentBlock,
) error {
	if strings.TrimSpace(builderName) == "" ||
		builderName != strings.TrimSpace(builderName) {
		return errors.New("subagent: Bound SeedBuilder must be non-empty and trimmed")
	}
	if strings.TrimSpace(title) == "" || title != strings.TrimSpace(title) {
		return errors.New("subagent: Bound title must be non-empty and trimmed")
	}
	if len(prompt) == 0 {
		return errors.New("subagent: Bound initial prompt is required")
	}
	return nil
}

func validateExtensionNames(names []string) error {
	seen := make(map[string]struct{}, len(names))
	for _, extensionNameValue := range names {
		if strings.TrimSpace(extensionNameValue) == "" ||
			extensionNameValue != strings.TrimSpace(extensionNameValue) {
			return errors.New(
				"subagent: Bound Extension name must be non-empty and trimmed",
			)
		}
		if _, duplicate := seen[extensionNameValue]; duplicate {
			return errors.New("subagent: Bound Extension name is duplicated")
		}
		seen[extensionNameValue] = struct{}{}
	}
	return nil
}

func validateBoundAgentOptions(options agent.Options) error {
	if options.MaxTokens != nil &&
		(*options.MaxTokens <= 0 || int64(*options.MaxTokens) > maxSafeInteger) {
		return errors.New(
			"subagent: Bound Agent maxTokens must be a positive safe integer",
		)
	}
	return nil
}

func decodeBoundJSON(rawValue []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(rawValue))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("subagent: Bound payload contains multiple JSON values")
		}
		return err
	}
	return nil
}

func cloneInt(source *int) *int {
	if source == nil {
		return nil
	}
	detached := *source
	return &detached
}

var BoundBindingEvent = session.DefineEvent[BoundBindingData](
	BoundBindingEventName,
)
var BoundConfigEvent = session.DefineEvent[BoundConfigData](
	BoundConfigEventName,
)
var BoundConfigAppliedEvent = session.DefineEvent[BoundConfigAppliedData](
	BoundConfigAppliedEventName,
)
var BoundMaterializationEvent = session.DefineEvent[BoundMaterializationData](
	BoundMaterializationEventName,
)
var BoundCursorEvent = session.DefineEvent[BoundCursor](
	BoundCursorEventName,
)

var _ json.Marshaler = BoundCreation{}
var _ json.Unmarshaler = (*BoundCreation)(nil)
var _ json.Marshaler = BoundConfigSnapshot{}
var _ json.Unmarshaler = (*BoundConfigSnapshot)(nil)
