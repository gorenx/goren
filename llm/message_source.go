package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf16"

	"github.com/gorenx/goren/internal/jsonvalue"
)

// ContextForm describes the semantic form of producer-supplied context.
type ContextForm string

const (
	ContextInstructions ContextForm = "instructions"
	ContextCatalog      ContextForm = "catalog"
	ContextSnapshot     ContextForm = "snapshot"
	ContextNotice       ContextForm = "notice"
	ContextRelay        ContextForm = "relay"
	ContextRecall       ContextForm = "recall"
)

// ContextSummaryMaxChars is the source-compatible notice-summary bound.
const ContextSummaryMaxChars = 120

// BoundContextSummary applies the transcript notice bound using UTF-16 code
// units, matching JavaScript string length for normal Unicode text.
func BoundContextSummary(summary string) string {
	units := utf16.Encode([]rune(summary))
	if len(units) <= ContextSummaryMaxChars {
		return summary
	}
	return string(utf16.Decode(units[:ContextSummaryMaxChars-1])) + "…"
}

// MessageSource is the merge-extensible producer-provenance contract.
type MessageSource interface {
	SourceKind() string
	CloneSource() (MessageSource, error)
}

// UserMessageSource identifies direct user input.
type UserMessageSource struct {
	Kind string `json:"kind"`
}

func (UserMessageSource) SourceKind() string { return "user" }

func (origin UserMessageSource) CloneSource() (MessageSource, error) {
	origin.Kind = "user"
	return origin, nil
}

// PluginMessageSource attributes context to a plugin and its semantic form.
type PluginMessageSource struct {
	Kind     string                   `json:"kind"`
	Plugin   string                   `json:"plugin"`
	Form     ContextForm              `json:"form,omitempty"`
	Sections []ContextSnapshotSection `json:"sections,omitempty"`
	Summary  string                   `json:"summary,omitempty"`
}

func (PluginMessageSource) SourceKind() string { return "plugin" }

func (origin PluginMessageSource) CloneSource() (MessageSource, error) {
	if origin.Plugin == "" {
		return nil, errors.New("llm: plugin message source needs a plugin name")
	}
	if err := validateContextForm(origin); err != nil {
		return nil, err
	}
	origin.Kind = "plugin"
	origin.Sections = append([]ContextSnapshotSection(nil), origin.Sections...)
	return origin, nil
}

// MarshalJSON keeps form-required fields present even when their values are empty.
func (origin PluginMessageSource) MarshalJSON() ([]byte, error) {
	detachedOrigin, err := origin.CloneSource()
	if err != nil {
		return nil, err
	}
	validated := detachedOrigin.(PluginMessageSource)
	wireValue := struct {
		Kind     string                    `json:"kind"`
		Plugin   string                    `json:"plugin"`
		Form     ContextForm               `json:"form,omitempty"`
		Sections *[]ContextSnapshotSection `json:"sections,omitempty"`
		Summary  *string                   `json:"summary,omitempty"`
	}{Kind: "plugin", Plugin: validated.Plugin, Form: validated.Form}
	if validated.Form == ContextSnapshot {
		sectionsCopy := append([]ContextSnapshotSection(nil), validated.Sections...)
		if sectionsCopy == nil {
			sectionsCopy = []ContextSnapshotSection{}
		}
		wireValue.Sections = &sectionsCopy
	}
	if validated.Form == ContextNotice {
		summaryCopy := validated.Summary
		wireValue.Summary = &summaryCopy
	}
	return json.Marshal(wireValue)
}

func validateContextForm(origin PluginMessageSource) error {
	switch origin.Form {
	case "", ContextInstructions, ContextCatalog, ContextRelay, ContextRecall:
		if len(origin.Sections) != 0 || origin.Summary != "" {
			return errors.New("llm: plugin context fields do not match its form")
		}
	case ContextSnapshot:
		if origin.Sections == nil || origin.Summary != "" {
			return errors.New("llm: snapshot context needs sections and no summary")
		}
	case ContextNotice:
		if len(origin.Sections) != 0 {
			return errors.New("llm: notice context cannot contain sections")
		}
	default:
		return fmt.Errorf("llm: unsupported context form %q", origin.Form)
	}
	return nil
}

// ModelMessageSource retains provider/model identity and optional replay state.
type ModelMessageSource struct {
	Kind        string          `json:"kind"`
	Provider    string          `json:"provider"`
	Model       string          `json:"model"`
	ReplayState json.RawMessage `json:"replayState,omitempty"`
}

func (ModelMessageSource) SourceKind() string { return "model" }

func (origin ModelMessageSource) CloneSource() (MessageSource, error) {
	if origin.Provider == "" || origin.Model == "" {
		return nil, errors.New("llm: model message source needs provider and model")
	}
	if len(origin.ReplayState) != 0 {
		detached, err := jsonvalue.Clone(origin.ReplayState)
		if err != nil {
			return nil, fmt.Errorf("llm: invalid model replayState: %w", err)
		}
		origin.ReplayState = detached
	}
	origin.Kind = "model"
	return origin, nil
}

// ToolMessageSource correlates one user-role result with its model call.
type ToolMessageSource struct {
	Kind   string `json:"kind"`
	CallID CallID `json:"callId"`
}

func (ToolMessageSource) SourceKind() string { return "tool" }

func (origin ToolMessageSource) CloneSource() (MessageSource, error) {
	if origin.CallID == "" {
		return nil, errors.New("llm: tool message source needs a callId")
	}
	origin.Kind = "tool"
	return origin, nil
}

// OpaqueMessageSource preserves a plugin-defined source across durable JSON.
type OpaqueMessageSource struct {
	kindName string
	rawValue json.RawMessage
}

// NewOpaqueMessageSource validates and snapshots one extension source.
func NewOpaqueMessageSource(kindName string, rawValue json.RawMessage) (OpaqueMessageSource, error) {
	if kindName == "" {
		return OpaqueMessageSource{}, errors.New("llm: opaque message source kind is empty")
	}
	detached, err := jsonvalue.Clone(rawValue)
	if err != nil || !jsonvalue.IsObject(detached) {
		return OpaqueMessageSource{}, errors.New("llm: opaque message source must be a lossless JSON object")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(detached, &fields); err != nil {
		return OpaqueMessageSource{}, err
	}
	var encodedKind string
	if err := json.Unmarshal(fields["kind"], &encodedKind); err != nil || encodedKind != kindName {
		return OpaqueMessageSource{}, errors.New("llm: opaque message source discriminant does not match")
	}
	return OpaqueMessageSource{kindName: kindName, rawValue: detached}, nil
}

func (origin OpaqueMessageSource) SourceKind() string { return origin.kindName }

func (origin OpaqueMessageSource) CloneSource() (MessageSource, error) {
	return NewOpaqueMessageSource(origin.kindName, origin.rawValue)
}

// MarshalJSON returns the original extension object.
func (origin OpaqueMessageSource) MarshalJSON() ([]byte, error) {
	if origin.kindName == "" || len(origin.rawValue) == 0 {
		return nil, errors.New("llm: invalid opaque message source")
	}
	return append([]byte(nil), origin.rawValue...), nil
}

func decodeMessageSource(rawValue json.RawMessage) (MessageSource, error) {
	if !jsonvalue.IsObject(rawValue) {
		return nil, errors.New("llm: message source must be an object")
	}
	var header struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(rawValue, &header); err != nil || header.Kind == "" {
		return nil, errors.New("llm: message source kind is missing")
	}
	switch header.Kind {
	case "user":
		var origin UserMessageSource
		if err := decodeStrict(rawValue, &origin); err == nil {
			return origin.CloneSource()
		}
		// MessageSourceMap is merge-extensible in the pinned TypeScript
		// contract. Host adapters add rpcId and clientTimeZone to a direct user
		// source, while core LLM code still needs only the kind. Keep those
		// extension fields losslessly outside the core source vocabulary.
		return NewOpaqueMessageSource(header.Kind, rawValue)
	case "plugin":
		var origin PluginMessageSource
		if err := decodeStrict(rawValue, &origin); err != nil {
			return nil, err
		}
		return origin.CloneSource()
	case "model":
		var origin ModelMessageSource
		if err := decodeStrict(rawValue, &origin); err != nil {
			return nil, err
		}
		return origin.CloneSource()
	case "tool":
		var origin ToolMessageSource
		if err := decodeStrict(rawValue, &origin); err != nil {
			return nil, err
		}
		return origin.CloneSource()
	default:
		return NewOpaqueMessageSource(header.Kind, rawValue)
	}
}
