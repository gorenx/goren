// Package bound defines the public global Bound Definition capability and its
// durable per-Session event contracts. Runtime implementation remains private
// under subagent/internal/bound.
package bound

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/tools"
)

const maximumSafeInteger = int64(1<<53 - 1)

// Draft is one caller-owned complete Definition candidate. Name is stable
// identity; later revisions cannot rename it.
type Draft struct {
	Name            string
	Enabled         bool
	SystemPrompt    string
	AgentOptions    *agent.Options
	MaxDepth        *int64
	ToolRestriction *tools.ToolRestriction
	Extensions      []string
}

// Definition is one committed complete global revision.
type Definition struct {
	Name            string
	Revision        int64
	Enabled         bool
	SystemPrompt    string
	AgentOptions    *agent.Options
	MaxDepth        *int64
	ToolRestriction *tools.ToolRestriction
	Extensions      []string
}

// Definitions manages the global Definition set.
type Definitions interface {
	List(context.Context) ([]Definition, error)
	Create(context.Context, Creation) (Definition, error)
	Replace(context.Context, Replacement) (Definition, error)
}

// NewDefinition validates and snapshots one complete revision.
func NewDefinition(candidate Draft, revision int64) (Definition, error) {
	if strings.TrimSpace(candidate.Name) == "" ||
		candidate.Name != strings.TrimSpace(candidate.Name) {
		return Definition{}, errors.New(
			"subagent/bound: Definition name must be non-empty and trimmed",
		)
	}
	if revision <= 0 || revision > maximumSafeInteger {
		return Definition{}, errors.New(
			"subagent/bound: Definition revision must be a positive safe integer",
		)
	}
	if strings.TrimSpace(candidate.SystemPrompt) == "" {
		return Definition{}, errors.New(
			"subagent/bound: Definition system prompt is required",
		)
	}
	if candidate.MaxDepth != nil &&
		(*candidate.MaxDepth < 0 || *candidate.MaxDepth > maximumSafeInteger) {
		return Definition{}, errors.New(
			"subagent/bound: maxDepth must be a non-negative safe integer",
		)
	}
	if candidate.AgentOptions != nil && candidate.AgentOptions.MaxTokens != nil &&
		(*candidate.AgentOptions.MaxTokens <= 0 ||
			int64(*candidate.AgentOptions.MaxTokens) > maximumSafeInteger) {
		return Definition{}, errors.New(
			"subagent/bound: Agent maxTokens must be a positive safe integer",
		)
	}
	restriction, err := cloneToolRestriction(candidate.ToolRestriction)
	if err != nil {
		return Definition{}, err
	}
	if err = validateExtensionNames(candidate.Extensions); err != nil {
		return Definition{}, err
	}
	return Definition{
		Name:            candidate.Name,
		Revision:        revision,
		Enabled:         candidate.Enabled,
		SystemPrompt:    candidate.SystemPrompt,
		AgentOptions:    cloneAgentOptions(candidate.AgentOptions),
		MaxDepth:        cloneInt64(candidate.MaxDepth),
		ToolRestriction: restriction,
		Extensions:      cloneStringsOrEmpty(candidate.Extensions),
	}, nil
}

// SnapshotDefinition validates and detaches a committed Definition.
func SnapshotDefinition(source Definition) (Definition, error) {
	return NewDefinition(
		Draft{
			Name:            source.Name,
			Enabled:         source.Enabled,
			SystemPrompt:    source.SystemPrompt,
			AgentOptions:    source.AgentOptions,
			MaxDepth:        source.MaxDepth,
			ToolRestriction: source.ToolRestriction,
			Extensions:      source.Extensions,
		},
		source.Revision,
	)
}

// SnapshotDraft validates and detaches one complete candidate.
func SnapshotDraft(source Draft) (Draft, error) {
	validated, err := NewDefinition(source, 1)
	if err != nil {
		return Draft{}, err
	}
	return draftFromDefinition(validated), nil
}

func draftFromDefinition(source Definition) Draft {
	restriction, err := cloneToolRestriction(source.ToolRestriction)
	if err != nil {
		panic(err)
	}
	return Draft{
		Name:            source.Name,
		Enabled:         source.Enabled,
		SystemPrompt:    source.SystemPrompt,
		AgentOptions:    cloneAgentOptions(source.AgentOptions),
		MaxDepth:        cloneInt64(source.MaxDepth),
		ToolRestriction: restriction,
		Extensions:      cloneStrings(source.Extensions),
	}
}

func cloneAgentOptions(source *agent.Options) *agent.Options {
	if source == nil {
		return nil
	}
	return &agent.Options{
		Provider:  source.Provider,
		Model:     source.Model,
		MaxTokens: cloneInt(source.MaxTokens),
	}
}

func cloneToolRestriction(
	restriction *tools.ToolRestriction,
) (*tools.ToolRestriction, error) {
	if restriction == nil {
		return nil, nil
	}
	if restriction.Allow == nil && restriction.Deny == nil {
		return nil, errors.New(
			"subagent/bound: toolRestriction must declare allow and/or deny",
		)
	}
	return &tools.ToolRestriction{
		Allow: cloneStrings(restriction.Allow),
		Deny:  cloneStrings(restriction.Deny),
	}, nil
}

func validateExtensionNames(names []string) error {
	// Key is one selected Extension registration name. The empty value records
	// set membership for duplicate rejection without changing input order.
	seen := make(map[string]struct{}, len(names))
	for _, extensionName := range names {
		if strings.TrimSpace(extensionName) == "" ||
			extensionName != strings.TrimSpace(extensionName) {
			return errors.New(
				"subagent/bound: Extension name must be non-empty and trimmed",
			)
		}
		if _, duplicate := seen[extensionName]; duplicate {
			return errors.New(
				"subagent/bound: Extension name is duplicated",
			)
		}
		seen[extensionName] = struct{}{}
	}
	return nil
}

func cloneStrings(source []string) []string {
	if source == nil {
		return nil
	}
	detached := make([]string, len(source))
	copy(detached, source)
	return detached
}

func cloneStringsOrEmpty(source []string) []string {
	if source == nil {
		return []string{}
	}
	return cloneStrings(source)
}

func cloneInt(source *int) *int {
	if source == nil {
		return nil
	}
	detached := *source
	return &detached
}

func cloneInt64(source *int64) *int64 {
	if source == nil {
		return nil
	}
	detached := *source
	return &detached
}

var definitionJSONCodec = sonic.Config{
	EscapeHTML:            true,
	SortMapKeys:           true,
	CompactMarshaler:      true,
	NoNullSliceOrMap:      true,
	UseUnicodeErrors:      true,
	DisallowUnknownFields: true,
	CopyString:            true,
	ValidateString:        true,
	CaseSensitive:         true,
}.Froze()

func (source Draft) MarshalJSON() ([]byte, error) {
	validated, err := SnapshotDraft(source)
	if err != nil {
		return nil, err
	}
	return encodeDefinitionFields(validated, nil)
}

func (target *Draft) UnmarshalJSON(rawValue []byte) error {
	wireValue, err := decodeDefinitionFields(rawValue, false)
	if err != nil {
		return err
	}
	validated, err := SnapshotDraft(wireValue.draft)
	if err != nil {
		return err
	}
	*target = validated
	return nil
}

func (source Definition) MarshalJSON() ([]byte, error) {
	validated, err := SnapshotDefinition(source)
	if err != nil {
		return nil, err
	}
	revision := validated.Revision
	return encodeDefinitionFields(draftFromDefinition(validated), &revision)
}

func (target *Definition) UnmarshalJSON(rawValue []byte) error {
	wireValue, err := decodeDefinitionFields(rawValue, true)
	if err != nil {
		return err
	}
	validated, err := NewDefinition(wireValue.draft, wireValue.revision)
	if err != nil {
		return err
	}
	*target = validated
	return nil
}

// jsonField distinguishes an omitted optional field from an explicit null.
// Sonic owns syntax and type decoding; this wrapper retains field presence for
// the Bound contract's complete-snapshot rules.
type jsonField[Value any] struct {
	value   Value
	present bool
	null    bool
}

func (field *jsonField[Value]) UnmarshalJSON(rawValue []byte) error {
	field.present = true
	if bytes.Equal(bytes.TrimSpace(rawValue), []byte("null")) {
		field.null = true
		return nil
	}
	return definitionJSONCodec.Unmarshal(rawValue, &field.value)
}

type agentOptionsFields struct {
	Provider  jsonField[string] `json:"provider"`
	Model     jsonField[string] `json:"model"`
	MaxTokens jsonField[int]    `json:"maxTokens"`
}

type toolRestrictionFields struct {
	Allow jsonField[[]string] `json:"allow"`
	Deny  jsonField[[]string] `json:"deny"`
}

type definitionFields struct {
	Name            *string                          `json:"name"`
	Revision        jsonField[int64]                 `json:"revision"`
	Enabled         *bool                            `json:"enabled"`
	SystemPrompt    *string                          `json:"systemPrompt"`
	AgentOptions    jsonField[agentOptionsFields]    `json:"agentOptions"`
	MaxDepth        jsonField[int64]                 `json:"maxDepth"`
	ToolRestriction jsonField[toolRestrictionFields] `json:"toolRestriction"`
	Extensions      *[]string                        `json:"extensions"`
}

type agentOptionsJSON struct {
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	MaxTokens *int   `json:"maxTokens,omitempty"`
}

type toolRestrictionJSON struct {
	Allow *[]string `json:"allow,omitempty"`
	Deny  *[]string `json:"deny,omitempty"`
}

type definitionJSON struct {
	Name            string               `json:"name"`
	Revision        *int64               `json:"revision,omitempty"`
	Enabled         bool                 `json:"enabled"`
	SystemPrompt    string               `json:"systemPrompt"`
	AgentOptions    *agentOptionsJSON    `json:"agentOptions,omitempty"`
	MaxDepth        *int64               `json:"maxDepth,omitempty"`
	ToolRestriction *toolRestrictionJSON `json:"toolRestriction,omitempty"`
	Extensions      []string             `json:"extensions"`
}

type decodedDefinitionFields struct {
	draft    Draft
	revision int64
}

func encodeDefinitionFields(candidate Draft, revision *int64) ([]byte, error) {
	restriction, err := encodeToolRestriction(candidate.ToolRestriction)
	if err != nil {
		return nil, err
	}
	return definitionJSONCodec.Marshal(
		definitionJSON{
			Name:            candidate.Name,
			Revision:        cloneInt64(revision),
			Enabled:         candidate.Enabled,
			SystemPrompt:    candidate.SystemPrompt,
			AgentOptions:    encodeAgentOptions(candidate.AgentOptions),
			MaxDepth:        cloneInt64(candidate.MaxDepth),
			ToolRestriction: restriction,
			Extensions:      cloneStringsOrEmpty(candidate.Extensions),
		},
	)
}

func decodeDefinitionFields(
	rawValue []byte,
	revisionRequired bool,
) (decodedDefinitionFields, error) {
	var wireValue definitionFields
	if err := decodeDefinitionJSON(rawValue, &wireValue); err != nil {
		return decodedDefinitionFields{}, err
	}
	if wireValue.Name == nil {
		return decodedDefinitionFields{}, errors.New(
			"subagent/bound: name is required",
		)
	}
	if wireValue.Enabled == nil {
		return decodedDefinitionFields{}, errors.New(
			"subagent/bound: enabled is required",
		)
	}
	if wireValue.SystemPrompt == nil {
		return decodedDefinitionFields{}, errors.New(
			"subagent/bound: systemPrompt is required",
		)
	}
	if wireValue.Extensions == nil {
		return decodedDefinitionFields{}, errors.New(
			"subagent/bound: extensions is required",
		)
	}
	if revisionRequired {
		if !wireValue.Revision.present || wireValue.Revision.null {
			return decodedDefinitionFields{}, errors.New(
				"subagent/bound: Definition revision is required",
			)
		}
	} else if wireValue.Revision.present {
		return decodedDefinitionFields{}, errors.New(
			"subagent/bound: Draft must not contain revision",
		)
	}
	agentOptions, err := decodeAgentOptions(wireValue.AgentOptions)
	if err != nil {
		return decodedDefinitionFields{}, err
	}
	maximumDepth, err := decodeMaximumDepth(wireValue.MaxDepth)
	if err != nil {
		return decodedDefinitionFields{}, err
	}
	restriction, err := decodeToolRestriction(wireValue.ToolRestriction)
	if err != nil {
		return decodedDefinitionFields{}, err
	}
	return decodedDefinitionFields{
		draft: Draft{
			Name:            *wireValue.Name,
			Enabled:         *wireValue.Enabled,
			SystemPrompt:    *wireValue.SystemPrompt,
			AgentOptions:    agentOptions,
			MaxDepth:        maximumDepth,
			ToolRestriction: restriction,
			Extensions:      cloneStrings(*wireValue.Extensions),
		},
		revision: wireValue.Revision.value,
	}, nil
}

func encodeAgentOptions(options *agent.Options) *agentOptionsJSON {
	if options == nil {
		return nil
	}
	return &agentOptionsJSON{
		Provider:  options.Provider,
		Model:     options.Model,
		MaxTokens: cloneInt(options.MaxTokens),
	}
}

func decodeAgentOptions(
	field jsonField[agentOptionsFields],
) (*agent.Options, error) {
	if !field.present {
		return nil, nil
	}
	if field.null {
		return nil, errors.New(
			"subagent/bound: agentOptions must be an object",
		)
	}
	if field.value.Provider.null {
		return nil, errors.New(
			"subagent/bound: agentOptions.provider must be a string",
		)
	}
	if field.value.Model.null {
		return nil, errors.New(
			"subagent/bound: agentOptions.model must be a string",
		)
	}
	if field.value.MaxTokens.null {
		return nil, errors.New(
			"subagent/bound: agentOptions.maxTokens must be an integer",
		)
	}
	options := &agent.Options{}
	if field.value.Provider.present {
		options.Provider = field.value.Provider.value
	}
	if field.value.Model.present {
		options.Model = field.value.Model.value
	}
	if field.value.MaxTokens.present {
		maximumTokens := field.value.MaxTokens.value
		options.MaxTokens = &maximumTokens
	}
	return options, nil
}

func decodeMaximumDepth(field jsonField[int64]) (*int64, error) {
	if !field.present {
		return nil, nil
	}
	if field.null {
		return nil, errors.New(
			"subagent/bound: maxDepth must be an integer",
		)
	}
	maximumDepth := field.value
	return &maximumDepth, nil
}

func encodeToolRestriction(
	restriction *tools.ToolRestriction,
) (*toolRestrictionJSON, error) {
	cloned, err := cloneToolRestriction(restriction)
	if err != nil || cloned == nil {
		return nil, err
	}
	wireValue := &toolRestrictionJSON{}
	if cloned.Allow != nil {
		allow := cloneStrings(cloned.Allow)
		wireValue.Allow = &allow
	}
	if cloned.Deny != nil {
		deny := cloneStrings(cloned.Deny)
		wireValue.Deny = &deny
	}
	return wireValue, nil
}

func decodeToolRestriction(
	field jsonField[toolRestrictionFields],
) (*tools.ToolRestriction, error) {
	if !field.present {
		return nil, nil
	}
	if field.null {
		return nil, errors.New(
			"subagent/bound: toolRestriction must be an object",
		)
	}
	if !field.value.Allow.present && !field.value.Deny.present {
		return nil, errors.New(
			"subagent/bound: toolRestriction must declare allow and/or deny",
		)
	}
	if field.value.Allow.null {
		return nil, errors.New(
			"subagent/bound: toolRestriction.allow must be an array of strings",
		)
	}
	if field.value.Deny.null {
		return nil, errors.New(
			"subagent/bound: toolRestriction.deny must be an array of strings",
		)
	}
	return &tools.ToolRestriction{
		Allow: clonePresentStrings(field.value.Allow),
		Deny:  clonePresentStrings(field.value.Deny),
	}, nil
}

func clonePresentStrings(field jsonField[[]string]) []string {
	if !field.present {
		return nil
	}
	return cloneStrings(field.value)
}

func decodeDefinitionJSON(rawValue []byte, target any) error {
	if err := definitionJSONCodec.Unmarshal(rawValue, target); err != nil {
		return fmt.Errorf("subagent/bound: invalid JSON: %w", err)
	}
	return nil
}
