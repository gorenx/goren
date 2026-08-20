// Package agentloop owns concrete Agent construction, lifecycle, and the
// default turn/step/tool-call driver over durable Sessions.
package agentloop

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
)

const (
	// PluginName is the canonical Harness Agent Loop Plugin name.
	PluginName = "@deepseek-ai/dsh-agent-loop"
	// ServiceName is the canonical Cordis service name.
	ServiceName = "agentLoop"
	// DefaultMaxParallelToolCalls is the source deployment-wide scheduler cap.
	DefaultMaxParallelToolCalls = 10
	maxSafeInteger              = int64(1<<53 - 1)
)

// ConfiguredAgent is one boot-time concrete Agent declaration.
type ConfiguredAgent struct {
	ID              string            `json:"id"`
	SessionID       session.SessionID `json:"sessionId,omitempty"`
	Provider        string            `json:"provider,omitempty"`
	Model           string            `json:"model,omitempty"`
	MaxTokens       *int              `json:"maxTokens,omitempty"`
	CWD             string            `json:"cwd,omitempty"`
	ResumeSessionID session.SessionID `json:"resumeSessionId,omitempty"`
}

// UnmarshalJSON rejects undeclared fields, explicit nulls, and an explicitly
// empty sessionId while preserving omission for optional fields.
func (entry *ConfiguredAgent) UnmarshalJSON(encoded []byte) error {
	if entry == nil {
		return errors.New("agentloop: cannot decode configured Agent into nil target")
	}
	if bytes.Equal(bytes.TrimSpace(encoded), []byte("null")) {
		return errors.New("agentloop: configured Agent must be an object")
	}
	var wireValue struct {
		ID              json.RawMessage `json:"id"`
		SessionID       json.RawMessage `json:"sessionId"`
		Provider        json.RawMessage `json:"provider"`
		Model           json.RawMessage `json:"model"`
		MaxTokens       json.RawMessage `json:"maxTokens"`
		CWD             json.RawMessage `json:"cwd"`
		ResumeSessionID json.RawMessage `json:"resumeSessionId"`
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wireValue); err != nil {
		return err
	}
	var decoded ConfiguredAgent
	if len(wireValue.ID) != 0 {
		if err := decodeConfiguredString(wireValue.ID, "id", &decoded.ID); err != nil {
			return err
		}
	}
	if len(wireValue.SessionID) != 0 {
		var identifier string
		if err := decodeConfiguredString(wireValue.SessionID, "sessionId", &identifier); err != nil {
			return err
		}
		if identifier == "" {
			return errors.New("agentloop: configured Agent sessionId must be non-empty when present")
		}
		decoded.SessionID = session.SessionID(identifier)
	}
	if len(wireValue.Provider) != 0 {
		if err := decodeConfiguredString(wireValue.Provider, "provider", &decoded.Provider); err != nil {
			return err
		}
	}
	if len(wireValue.Model) != 0 {
		if err := decodeConfiguredString(wireValue.Model, "model", &decoded.Model); err != nil {
			return err
		}
	}
	if len(wireValue.MaxTokens) != 0 {
		if bytes.Equal(bytes.TrimSpace(wireValue.MaxTokens), []byte("null")) {
			return errors.New("agentloop: configured Agent maxTokens must be a positive safe integer")
		}
		var tokenLimit int
		if err := json.Unmarshal(wireValue.MaxTokens, &tokenLimit); err != nil {
			return fmt.Errorf("agentloop: configured Agent maxTokens must be a positive safe integer: %w", err)
		}
		decoded.MaxTokens = &tokenLimit
	}
	if len(wireValue.CWD) != 0 {
		if err := decodeConfiguredString(wireValue.CWD, "cwd", &decoded.CWD); err != nil {
			return err
		}
	}
	if len(wireValue.ResumeSessionID) != 0 {
		var identifier string
		if err := decodeConfiguredString(wireValue.ResumeSessionID, "resumeSessionId", &identifier); err != nil {
			return err
		}
		decoded.ResumeSessionID = session.SessionID(identifier)
	}
	*entry = decoded
	return nil
}

func decodeConfiguredString(encoded json.RawMessage, fieldName string, destination *string) error {
	if bytes.Equal(bytes.TrimSpace(encoded), []byte("null")) {
		return fmt.Errorf("agentloop: configured Agent %s must be a string", fieldName)
	}
	if err := json.Unmarshal(encoded, destination); err != nil {
		return fmt.Errorf("agentloop: configured Agent %s must be a string: %w", fieldName, err)
	}
	return nil
}

// AgentOptions returns the provider-neutral runtime options in this declaration.
func (entry ConfiguredAgent) AgentOptions() agent.Options {
	return agent.Options{
		Provider: entry.Provider, Model: entry.Model, MaxTokens: cloneInt(entry.MaxTokens),
	}
}

// Config is the strict typed Agent Loop plugin configuration.
type Config struct {
	MaxParallelToolCalls *int              `json:"maxParallelToolCalls,omitempty"`
	Agents               []ConfiguredAgent `json:"agents,omitempty"`
}

type configWire struct {
	MaxParallelToolCalls json.RawMessage `json:"maxParallelToolCalls"`
	Agents               json.RawMessage `json:"agents"`
}

// UnmarshalJSON preserves omission while rejecting null and unknown fields.
func (settings *Config) UnmarshalJSON(encoded []byte) error {
	if settings == nil {
		return errors.New("agentloop: cannot decode config into nil target")
	}
	if bytes.Equal(bytes.TrimSpace(encoded), []byte("null")) {
		return errors.New("agentloop: config must be an object")
	}
	var wireValue configWire
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wireValue); err != nil {
		return err
	}
	var decoded Config
	if len(wireValue.MaxParallelToolCalls) != 0 {
		if bytes.Equal(bytes.TrimSpace(wireValue.MaxParallelToolCalls), []byte("null")) {
			return errors.New("agentloop: maxParallelToolCalls must be a positive integer")
		}
		var parallelLimit int
		if err := json.Unmarshal(wireValue.MaxParallelToolCalls, &parallelLimit); err != nil {
			return fmt.Errorf("agentloop: maxParallelToolCalls must be a positive integer: %w", err)
		}
		decoded.MaxParallelToolCalls = &parallelLimit
	}
	if len(wireValue.Agents) != 0 {
		if bytes.Equal(bytes.TrimSpace(wireValue.Agents), []byte("null")) {
			return errors.New("agentloop: agents must be an array")
		}
		if err := json.Unmarshal(wireValue.Agents, &decoded.Agents); err != nil {
			return fmt.Errorf("agentloop: agents must be an array: %w", err)
		}
	}
	*settings = decoded
	return nil
}

// ValidatedConfig is a detached configuration safe for runtime ownership.
type ValidatedConfig struct {
	maxParallelToolCalls int
	agents               []ConfiguredAgent
}

// MaxParallelToolCalls returns the bounded dispatch pool size.
func (settings ValidatedConfig) MaxParallelToolCalls() int { return settings.maxParallelToolCalls }

// ConfiguredAgents returns a detached boot-time declaration snapshot.
func (settings ValidatedConfig) ConfiguredAgents() []ConfiguredAgent {
	return cloneConfiguredAgents(settings.agents)
}

// ValidateConfig defaults and validates all boot-time declarations before any lifecycle starts.
func ValidateConfig(settings Config) (ValidatedConfig, error) {
	parallelLimit := DefaultMaxParallelToolCalls
	if settings.MaxParallelToolCalls != nil {
		parallelLimit = *settings.MaxParallelToolCalls
	}
	if parallelLimit < 1 {
		return ValidatedConfig{}, errors.New("agentloop: maxParallelToolCalls must be a positive integer")
	}
	configured := cloneConfiguredAgents(settings.Agents)
	exactIdentities := make(map[session.SessionID]string)
	for index := range configured {
		entry := &configured[index]
		if strings.TrimSpace(entry.ID) == "" || entry.ID != strings.TrimSpace(entry.ID) {
			return ValidatedConfig{}, fmt.Errorf("agentloop: agents[%d].id must be non-empty and trimmed", index)
		}
		if entry.SessionID != "" && entry.ResumeSessionID != "" {
			return ValidatedConfig{}, fmt.Errorf("agentloop: agent %q sessionId and resumeSessionId are mutually exclusive", entry.ID)
		}
		if entry.MaxTokens != nil && (*entry.MaxTokens <= 0 || int64(*entry.MaxTokens) > maxSafeInteger) {
			return ValidatedConfig{}, fmt.Errorf("agentloop: agent %q maxTokens must be a positive safe integer", entry.ID)
		}
		exactIdentity := entry.SessionID
		if entry.ResumeSessionID != "" {
			exactIdentity = entry.ResumeSessionID
		}
		if exactIdentity == "" {
			continue
		}
		if firstLabel, exists := exactIdentities[exactIdentity]; exists {
			return ValidatedConfig{}, fmt.Errorf(
				"agentloop: agents %q and %q use duplicate exact session identity %q",
				firstLabel, entry.ID, exactIdentity,
			)
		}
		exactIdentities[exactIdentity] = entry.ID
	}
	return ValidatedConfig{
		maxParallelToolCalls: parallelLimit,
		agents:               configured,
	}, nil
}

func cloneConfiguredAgents(entries []ConfiguredAgent) []ConfiguredAgent {
	if entries == nil {
		return nil
	}
	detached := make([]ConfiguredAgent, len(entries))
	for index, entry := range entries {
		detached[index] = entry
		detached[index].MaxTokens = cloneInt(entry.MaxTokens)
	}
	return detached
}

func cloneInt(source *int) *int {
	if source == nil {
		return nil
	}
	copyValue := *source
	return &copyValue
}
