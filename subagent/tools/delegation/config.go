package delegation

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/internal/jsonvalue"
	"github.com/gorenx/goren/tools"
)

// BackgroundMode selects the lifecycle used when a call runs in background.
type BackgroundMode string

const (
	// BackgroundOneShot selects generic Jobs collection, excluded in Goren.
	BackgroundOneShot BackgroundMode = "one-shot"
	// BackgroundContinuable selects a durable continuable child.
	BackgroundContinuable BackgroundMode = "continuable"
)

// Settings contains validated operational policy for one delegation Tool.
// SeedBuilder is the exact registered child Session seed strategy.
type Settings struct {
	SeedBuilder           string
	ToolName              string
	EnableRunInBackground bool
	BackgroundMode        BackgroundMode
	AgentOptions          *agent.Options
	Persona               *string
	ToolFilter            *tools.ToolRestriction
	MaxDepth              *int64
}

// Config is the strict deployment vocabulary of one delegation Tool.
type Config struct {
	Provider              string         `json:"provider"`
	ToolName              string         `json:"toolName"`
	EnableRunInBackground bool           `json:"enableRunInBackground"`
	BackgroundMode        BackgroundMode `json:"backgroundMode"`
	AgentOptions          *AgentOptions  `json:"agentOptions,omitempty"`
	Persona               *string        `json:"persona,omitempty"`
	ToolFilter            *ToolFilter    `json:"toolFilter,omitempty"`
	MaxDepth              DepthLimit     `json:"maxDepth"`
}

// AgentOptions is the provider-neutral child model configuration.
type AgentOptions struct {
	Provider  string `json:"provider,omitempty"`
	Model     string `json:"model,omitempty"`
	MaxTokens *int   `json:"maxTokens,omitempty"`
}

// ToolFilter controls inherited Tool visibility in each child.
type ToolFilter struct {
	Allow *[]string `json:"allow,omitempty"`
	Deny  *[]string `json:"deny,omitempty"`
}

// DepthLimit is the canonical maxDepth wire value. A number is a local
// recursion cap; "provider-managed" means this Tool contributes no cap.
type DepthLimit struct {
	kind  depthLimitKind
	value int64
}

type depthLimitKind uint8

const (
	depthLimitNumeric depthLimitKind = iota
	depthLimitUnspecified
)

// NewNumericDepthLimit constructs a validated numeric maxDepth configuration.
func NewNumericDepthLimit(value int64) (DepthLimit, error) {
	if value < 0 || value > 1<<53-1 {
		return DepthLimit{}, errors.New(
			"subagent tool factory: maxDepth must be a non-negative safe integer",
		)
	}
	return DepthLimit{
		kind:  depthLimitNumeric,
		value: value,
	}, nil
}

// UnmarshalJSON decodes the canonical number | "provider-managed" union.
func (limit *DepthLimit) UnmarshalJSON(rawValue []byte) error {
	if limit == nil {
		return errors.New("subagent tool factory: nil maxDepth target")
	}
	if bytes.Equal(bytes.TrimSpace(rawValue), []byte(`"provider-managed"`)) {
		*limit = DepthLimit{
			kind: depthLimitUnspecified,
		}
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(rawValue))
	decoder.UseNumber()
	var numeric json.Number
	if decodeErr := decoder.Decode(&numeric); decodeErr != nil {
		return errors.New(
			"subagent tool factory: maxDepth must be a non-negative integer or provider-managed",
		)
	}
	value, parseErr := numeric.Int64()
	if parseErr != nil {
		return errors.New(
			"subagent tool factory: maxDepth must be a non-negative safe integer",
		)
	}
	configuredLimit, numericErr := NewNumericDepthLimit(value)
	if numericErr != nil {
		return numericErr
	}
	*limit = configuredLimit
	return nil
}

// MarshalJSON preserves the canonical number | "provider-managed" union.
func (limit DepthLimit) MarshalJSON() ([]byte, error) {
	if limit.kind == depthLimitUnspecified {
		return []byte(`"provider-managed"`), nil
	}
	if limit.kind != depthLimitNumeric || limit.value < 0 || limit.value > 1<<53-1 {
		return nil, errors.New(
			"subagent tool factory: maxDepth state is invalid",
		)
	}
	return json.Marshal(limit.value)
}

// DefaultConfig returns the existing deployment defaults. Provider remains required.
func DefaultConfig() Config {
	return Config{
		ToolName:              DefaultToolName,
		EnableRunInBackground: true,
		BackgroundMode:        BackgroundOneShot,
		MaxDepth: DepthLimit{
			kind:  depthLimitNumeric,
			value: 3,
		},
	}
}

// Resolve maps typed configuration into validated, detached runtime settings.
func (configured Config) Resolve() (Settings, error) {
	resolved := Settings{
		SeedBuilder:           configured.Provider,
		ToolName:              configured.ToolName,
		EnableRunInBackground: configured.EnableRunInBackground,
		BackgroundMode:        configured.BackgroundMode,
		Persona:               configured.Persona,
	}
	if configured.AgentOptions != nil {
		resolved.AgentOptions = &agent.Options{
			Provider:  configured.AgentOptions.Provider,
			Model:     configured.AgentOptions.Model,
			MaxTokens: configured.AgentOptions.MaxTokens,
		}
	}
	if configured.ToolFilter != nil {
		resolved.ToolFilter = &tools.ToolRestriction{}
		if configured.ToolFilter.Allow != nil {
			resolved.ToolFilter.Allow = make([]string, len(*configured.ToolFilter.Allow))
			copy(resolved.ToolFilter.Allow, *configured.ToolFilter.Allow)
		}
		if configured.ToolFilter.Deny != nil {
			resolved.ToolFilter.Deny = make([]string, len(*configured.ToolFilter.Deny))
			copy(resolved.ToolFilter.Deny, *configured.ToolFilter.Deny)
		}
	}
	if configured.MaxDepth.kind == depthLimitNumeric {
		maxDepth := configured.MaxDepth.value
		resolved.MaxDepth = &maxDepth
	}
	if err := validateSettings(resolved); err != nil {
		return Settings{}, err
	}
	return cloneSettings(resolved), nil
}

func validateSettings(toolSettings Settings) error {
	if strings.TrimSpace(toolSettings.SeedBuilder) == "" ||
		toolSettings.SeedBuilder != strings.TrimSpace(toolSettings.SeedBuilder) {
		return errors.New(
			"subagent tool: SeedBuilder name must be non-empty and trimmed",
		)
	}
	if strings.TrimSpace(toolSettings.ToolName) == "" ||
		toolSettings.ToolName != strings.TrimSpace(toolSettings.ToolName) {
		return errors.New("subagent tool: toolName must be non-empty and trimmed")
	}
	switch toolSettings.BackgroundMode {
	case BackgroundOneShot, BackgroundContinuable:
	default:
		return fmt.Errorf(
			"subagent tool: unsupported backgroundMode %q",
			toolSettings.BackgroundMode,
		)
	}
	if toolSettings.MaxDepth != nil &&
		(*toolSettings.MaxDepth < 0 || *toolSettings.MaxDepth > 1<<53-1) {
		return errors.New(
			"subagent tool: maxDepth must be a non-negative safe integer",
		)
	}
	if toolSettings.ToolFilter != nil &&
		toolSettings.ToolFilter.Allow == nil && toolSettings.ToolFilter.Deny == nil {
		return errors.New(
			"subagent tool: toolFilter must declare allow and/or deny",
		)
	}
	return nil
}

func cloneSettings(source Settings) Settings {
	detached := source
	if source.AgentOptions != nil {
		childOptions := *source.AgentOptions
		if source.AgentOptions.MaxTokens != nil {
			maxTokensValue := *source.AgentOptions.MaxTokens
			childOptions.MaxTokens = &maxTokensValue
		}
		detached.AgentOptions = &childOptions
	}
	if source.Persona != nil {
		personaText := *source.Persona
		detached.Persona = &personaText
	}
	if source.ToolFilter != nil {
		detached.ToolFilter = &tools.ToolRestriction{
			Allow: cloneStrings(source.ToolFilter.Allow),
			Deny:  cloneStrings(source.ToolFilter.Deny),
		}
	}
	if source.MaxDepth != nil {
		maxDepthValue := *source.MaxDepth
		detached.MaxDepth = &maxDepthValue
	}
	return detached
}

func cloneStrings(source []string) []string {
	if source == nil {
		return nil
	}
	detached := make([]string, len(source))
	copy(detached, source)
	return detached
}

// DecodeConfig decodes exactly one strict configuration object and resolves defaults.
func DecodeConfig(rawConfig json.RawMessage) (Settings, error) {
	if err := jsonvalue.Validate(rawConfig); err != nil {
		return Settings{}, fmt.Errorf("subagent tool: invalid configuration: %w", err)
	}
	trimmed := bytes.TrimSpace(rawConfig)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return Settings{}, errors.New("subagent tool: configuration must be a JSON object")
	}
	settings := DefaultConfig()
	decoder := json.NewDecoder(bytes.NewReader(rawConfig))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		return Settings{}, fmt.Errorf("subagent tool: decode configuration: %w", err)
	}
	return settings.Resolve()
}
