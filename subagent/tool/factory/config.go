package factory

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gorenx/goren/agent"
	pluginfactory "github.com/gorenx/goren/plugin/factory"
	"github.com/gorenx/goren/subagent/tool"
	"github.com/gorenx/goren/tools"
)

// Config is the strict deployment vocabulary of one delegation Tool.
type Config struct {
	Provider              string              `json:"provider"`
	ToolName              string              `json:"toolName"`
	EnableRunInBackground bool                `json:"enableRunInBackground"`
	BackgroundMode        tool.BackgroundMode `json:"backgroundMode"`
	AgentOptions          *AgentOptions       `json:"agentOptions,omitempty"`
	Persona               *string             `json:"persona,omitempty"`
	ToolFilter            *ToolFilter         `json:"toolFilter,omitempty"`
	MaxDepth              DepthLimit          `json:"maxDepth"`
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
	ProviderManaged bool
	Value           int64
}

// UnmarshalJSON decodes the canonical number | "provider-managed" union.
func (limit *DepthLimit) UnmarshalJSON(rawValue []byte) error {
	if limit == nil {
		return errors.New("subagent tool factory: nil maxDepth target")
	}
	if bytes.Equal(bytes.TrimSpace(rawValue), []byte(`"provider-managed"`)) {
		*limit = DepthLimit{
			ProviderManaged: true,
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
	if parseErr != nil || value < 0 || value > 1<<53-1 {
		return errors.New(
			"subagent tool factory: maxDepth must be a non-negative safe integer",
		)
	}
	*limit = DepthLimit{
		Value: value,
	}
	return nil
}

// MarshalJSON preserves the canonical number | "provider-managed" union.
func (limit DepthLimit) MarshalJSON() ([]byte, error) {
	if limit.ProviderManaged {
		return []byte(`"provider-managed"`), nil
	}
	if limit.Value < 0 || limit.Value > 1<<53-1 {
		return nil, errors.New(
			"subagent tool factory: maxDepth must be a non-negative safe integer",
		)
	}
	return json.Marshal(limit.Value)
}

func decodeConfig(rawConfig json.RawMessage) (tool.Settings, error) {
	if configErr := pluginfactory.ValidateObjectConfig(
		rawConfig,
		"subagent tool factory",
	); configErr != nil {
		return tool.Settings{}, configErr
	}
	settings := Config{
		ToolName:              tool.DefaultToolName,
		EnableRunInBackground: true,
		BackgroundMode:        tool.BackgroundOneShot,
		MaxDepth: DepthLimit{
			Value: 3,
		},
	}
	decoder := json.NewDecoder(bytes.NewReader(rawConfig))
	decoder.DisallowUnknownFields()
	if decodeErr := decoder.Decode(&settings); decodeErr != nil {
		return tool.Settings{}, fmt.Errorf(
			"subagent tool factory: decode configuration: %w",
			decodeErr,
		)
	}
	resolved := tool.Settings{
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
	if !settings.MaxDepth.ProviderManaged {
		resolved.MaxDepth = &settings.MaxDepth.Value
	}
	return resolved, nil
}
