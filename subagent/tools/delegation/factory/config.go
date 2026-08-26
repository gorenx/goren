package factory

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gorenx/goren/agent"
	pluginfactory "github.com/gorenx/goren/plugin/factory"
	"github.com/gorenx/goren/subagent/tools/delegation"
	"github.com/gorenx/goren/tools"
)

// Config is the strict deployment vocabulary of one delegation Tool.
type Config struct {
	Provider              string                    `json:"provider"`
	ToolName              string                    `json:"toolName"`
	EnableRunInBackground bool                      `json:"enableRunInBackground"`
	BackgroundMode        delegation.BackgroundMode `json:"backgroundMode"`
	AgentOptions          *AgentOptions             `json:"agentOptions,omitempty"`
	Persona               *string                   `json:"persona,omitempty"`
	ToolFilter            *ToolFilter               `json:"toolFilter,omitempty"`
	MaxDepth              DepthLimit                `json:"maxDepth"`
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

// DepthLimit is either a numeric recursion cap or provider-managed policy.
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

func decodeConfig(rawConfig json.RawMessage) (delegation.Settings, error) {
	if configErr := pluginfactory.ValidateObjectConfig(
		rawConfig,
		"subagent tool factory",
	); configErr != nil {
		return delegation.Settings{}, configErr
	}
	settings := Config{
		ToolName:              delegation.DefaultToolName,
		EnableRunInBackground: true,
		BackgroundMode:        delegation.BackgroundOneShot,
		MaxDepth: DepthLimit{
			kind:  depthLimitNumeric,
			value: 3,
		},
	}
	decoder := json.NewDecoder(bytes.NewReader(rawConfig))
	decoder.DisallowUnknownFields()
	if decodeErr := decoder.Decode(&settings); decodeErr != nil {
		return delegation.Settings{}, fmt.Errorf(
			"subagent tool factory: decode configuration: %w",
			decodeErr,
		)
	}
	resolved := delegation.Settings{
		Provider:              settings.Provider,
		ToolName:              settings.ToolName,
		EnableRunInBackground: settings.EnableRunInBackground,
		BackgroundMode:        settings.BackgroundMode,
		Persona:               settings.Persona,
	}
	if settings.AgentOptions != nil {
		resolved.AgentOptions = &agent.Options{
			Provider:  settings.AgentOptions.Provider,
			Model:     settings.AgentOptions.Model,
			MaxTokens: settings.AgentOptions.MaxTokens,
		}
	}
	if settings.ToolFilter != nil {
		resolved.ToolFilter = &tools.ToolRestriction{}
		if settings.ToolFilter.Allow != nil {
			resolved.ToolFilter.Allow = append(
				[]string(nil),
				(*settings.ToolFilter.Allow)...,
			)
		}
		if settings.ToolFilter.Deny != nil {
			resolved.ToolFilter.Deny = append(
				[]string(nil),
				(*settings.ToolFilter.Deny)...,
			)
		}
	}
	if settings.MaxDepth.kind == depthLimitNumeric {
		maxDepth := settings.MaxDepth.value
		resolved.MaxDepth = &maxDepth
	}
	return resolved, nil
}
