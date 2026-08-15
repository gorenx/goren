// Package sessiontitle owns log-backed Session titles, deterministic fallback
// generation, provider scheduling, and the title projection unit.
package sessiontitle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
)

const (
	ServiceName    = "sessionTitle"
	TitleEventName = "session/title"
	ProjectionKey  = "title"
)

// Service is the canonical session-title capability identity.
var Service = plugin.DefineService[TitleService](ServiceName)

// TitleSet is the private typed identity of the log-only session/title event.
var TitleSet = session.DefineEvent[EventData](TitleEventName)

// ProviderID identifies the provider recorded in accepted title provenance.
type ProviderID string

// ModelProvenance identifies the exact auxiliary model route used for a title.
type ModelProvenance struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// TitleSource is the closed provenance contract of one accepted title.
type TitleSource interface {
	SourceKind() string
	cloneTitleSource() TitleSource
}

// FallbackSource marks a deterministic built-in fallback.
type FallbackSource struct {
	Kind string `json:"kind"`
}

func (FallbackSource) SourceKind() string { return "fallback" }

func (FallbackSource) cloneTitleSource() TitleSource { return FallbackSource{Kind: "fallback"} }

// ProviderSource records an optional asynchronous provider and model route.
type ProviderSource struct {
	Kind     string           `json:"kind"`
	Provider ProviderID       `json:"provider"`
	Model    *ModelProvenance `json:"model,omitempty"`
}

func (ProviderSource) SourceKind() string { return "provider" }

func (source ProviderSource) cloneTitleSource() TitleSource {
	result := ProviderSource{Kind: "provider", Provider: source.Provider}
	if source.Model != nil {
		modelRoute := *source.Model
		result.Model = &modelRoute
	}
	return result
}

// UserSource marks an explicit rename that pins automatic generation.
type UserSource struct {
	Kind string `json:"kind"`
}

func (UserSource) SourceKind() string { return "user" }

func (UserSource) cloneTitleSource() TitleSource { return UserSource{Kind: "user"} }

// EventData is the whole state carried by one log-only session/title event.
type EventData struct {
	Title       string      `json:"title"`
	MessageSeqs []int64     `json:"messageSeqs"`
	Source      TitleSource `json:"source"`
}

// MarshalJSON preserves the source discriminated union and detached seqs.
func (payload EventData) MarshalJSON() ([]byte, error) {
	if err := validateEventData(payload); err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Title       string      `json:"title"`
		MessageSeqs []int64     `json:"messageSeqs"`
		Source      TitleSource `json:"source"`
	}{Title: payload.Title, MessageSeqs: append([]int64{}, payload.MessageSeqs...), Source: payload.Source.cloneTitleSource()})
}

// UnmarshalJSON restores and validates the closed source union.
func (payload *EventData) UnmarshalJSON(rawValue []byte) error {
	if payload == nil {
		return errors.New("sessiontitle: cannot decode EventData into nil target")
	}
	var wireValue struct {
		Title       string          `json:"title"`
		MessageSeqs json.RawMessage `json:"messageSeqs"`
		Source      json.RawMessage `json:"source"`
	}
	if err := decodeStrict(rawValue, &wireValue); err != nil {
		return fmt.Errorf("sessiontitle: invalid title event: %w", err)
	}
	var kindValue struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(wireValue.Source, &kindValue); err != nil {
		return fmt.Errorf("sessiontitle: invalid title source: %w", err)
	}
	if !bytes.HasPrefix(bytes.TrimSpace(wireValue.MessageSeqs), []byte("[")) {
		return errors.New("sessiontitle: title event messageSeqs must be an array")
	}
	var messageSeqs []int64
	if err := decodeStrict(wireValue.MessageSeqs, &messageSeqs); err != nil {
		return fmt.Errorf("sessiontitle: invalid title event messageSeqs: %w", err)
	}
	var decodedSource TitleSource
	switch kindValue.Kind {
	case "fallback":
		var source FallbackSource
		if err := decodeStrict(wireValue.Source, &source); err != nil {
			return fmt.Errorf("sessiontitle: invalid fallback source: %w", err)
		}
		decodedSource = FallbackSource{Kind: "fallback"}
	case "provider":
		var source ProviderSource
		if err := decodeStrict(wireValue.Source, &source); err != nil {
			return fmt.Errorf("sessiontitle: invalid provider source: %w", err)
		}
		source.Kind = "provider"
		decodedSource = source
	case "user":
		var source UserSource
		if err := decodeStrict(wireValue.Source, &source); err != nil {
			return fmt.Errorf("sessiontitle: invalid user source: %w", err)
		}
		decodedSource = UserSource{Kind: "user"}
	default:
		return fmt.Errorf("sessiontitle: unsupported title source %q", kindValue.Kind)
	}
	decoded := EventData{
		Title: wireValue.Title, MessageSeqs: append([]int64{}, messageSeqs...), Source: decodedSource,
	}
	if err := validateEventData(decoded); err != nil {
		return err
	}
	*payload = decoded
	return nil
}

// Snapshot is the latest title event plus its durable envelope facts.
type Snapshot struct {
	EventData
	EventSeq  int64 `json:"eventSeq"`
	UpdatedAt int64 `json:"updatedAt"`
}

// Config contains required deterministic fallback and accepted-title limits.
type Config struct {
	FallbackMaxWords int `json:"fallbackMaxWords"`
	FallbackMaxBytes int `json:"fallbackMaxBytes"`
	MaxTitleBytes    int `json:"maxTitleBytes"`
}

// Validate resolves and checks typed title configuration.
func (settings Config) Validate() (Config, error) {
	if settings.FallbackMaxWords <= 0 {
		return Config{}, errors.New("sessiontitle: fallbackMaxWords must be a positive integer")
	}
	if settings.FallbackMaxBytes <= 0 {
		return Config{}, errors.New("sessiontitle: fallbackMaxBytes must be a positive integer")
	}
	if settings.MaxTitleBytes <= 0 {
		return Config{}, errors.New("sessiontitle: maxTitleBytes must be a positive integer")
	}
	if settings.FallbackMaxBytes > settings.MaxTitleBytes {
		return Config{}, errors.New("sessiontitle: fallbackMaxBytes must not exceed maxTitleBytes")
	}
	return settings, nil
}

// SessionTitleInvalidError identifies explicit rename input that normalizes
// to empty; liveness and lifecycle failures intentionally use other errors.
type SessionTitleInvalidError struct {
	Message string
}

func (problem *SessionTitleInvalidError) Error() string {
	if problem == nil || problem.Message == "" {
		return "session title must contain visible characters"
	}
	return problem.Message
}

// TitleService is the log-backed title read/write and provider-registration contract.
type TitleService interface {
	Get(*session.Session) (*Snapshot, error)
	Rename(*session.Session, string) (*Snapshot, error)
	Refresh(context.Context, *session.Session) (*Snapshot, error)
	Register(*plugin.Scope, Provider) (plugin.Disposer, error)
}

func validateEventData(payload EventData) error {
	if strings.TrimSpace(payload.Title) == "" || payload.Title != strings.TrimSpace(payload.Title) {
		return errors.New("sessiontitle: title event text must be non-empty and normalized")
	}
	if payload.Source == nil {
		return errors.New("sessiontitle: title event source is absent")
	}
	previous := int64(-1)
	for _, sequence := range payload.MessageSeqs {
		if sequence < 0 || sequence <= previous {
			return errors.New("sessiontitle: title event messageSeqs must be unique ordered non-negative seqs")
		}
		previous = sequence
	}
	switch source := payload.Source.(type) {
	case FallbackSource:
		if len(payload.MessageSeqs) != 1 {
			return errors.New("sessiontitle: fallback title requires one source message seq")
		}
	case ProviderSource:
		if source.Provider == "" || len(payload.MessageSeqs) == 0 {
			return errors.New("sessiontitle: provider title requires provider and source message seqs")
		}
		if source.Model != nil && (source.Model.Provider == "" || source.Model.Model == "") {
			return errors.New("sessiontitle: provider model provenance is incomplete")
		}
	case UserSource:
		if len(payload.MessageSeqs) != 0 {
			return errors.New("sessiontitle: user title must not claim source message seqs")
		}
	default:
		return fmt.Errorf("sessiontitle: unsupported title source %T", payload.Source)
	}
	return nil
}

func decodeStrict[T any](rawValue []byte, target *T) error {
	decoder := json.NewDecoder(bytes.NewReader(rawValue))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}
