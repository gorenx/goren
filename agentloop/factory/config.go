package factory

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentloop"
	"github.com/gorenx/goren/session"
)

// AgentConfig is one boot-time Agent declaration on the configuration wire.
type AgentConfig struct {
	ID              string
	SessionID       session.SessionID
	Provider        string
	Model           string
	MaxTokens       *int
	CWD             *string
	ResumeSessionID session.SessionID
}

// Config is the strict Agent Loop plugin configuration owned by this Factory.
type Config struct {
	MaxParallelToolCalls *int
	Agents               []AgentConfig
}

func (settings *Config) UnmarshalJSON(encoded []byte) error {
	if settings == nil {
		return errors.New("agentloop factory: cannot decode into nil Config")
	}
	if bytes.Equal(bytes.TrimSpace(encoded), []byte("null")) {
		return errors.New("agentloop factory: configuration must be an object")
	}
	var wireValue struct {
		MaxParallelToolCalls json.RawMessage `json:"maxParallelToolCalls"`
		Agents               json.RawMessage `json:"agents"`
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wireValue); err != nil {
		return err
	}
	var decoded Config
	if len(wireValue.MaxParallelToolCalls) != 0 {
		if isNull(wireValue.MaxParallelToolCalls) {
			return errors.New(
				"agentloop factory: maxParallelToolCalls must be a positive integer",
			)
		}
		var parallelLimit int
		if err := json.Unmarshal(
			wireValue.MaxParallelToolCalls,
			&parallelLimit,
		); err != nil {
			return fmt.Errorf(
				"agentloop factory: maxParallelToolCalls must be a positive integer: %w",
				err,
			)
		}
		decoded.MaxParallelToolCalls = &parallelLimit
	}
	if len(wireValue.Agents) != 0 {
		if isNull(wireValue.Agents) {
			return errors.New("agentloop factory: agents must be an array")
		}
		if err := json.Unmarshal(wireValue.Agents, &decoded.Agents); err != nil {
			return fmt.Errorf(
				"agentloop factory: agents must be an array: %w",
				err,
			)
		}
	}
	*settings = decoded
	return nil
}

func (entry *AgentConfig) UnmarshalJSON(encoded []byte) error {
	if entry == nil {
		return errors.New("agentloop factory: cannot decode into nil AgentConfig")
	}
	if bytes.Equal(bytes.TrimSpace(encoded), []byte("null")) {
		return errors.New("agentloop factory: Agent declaration must be an object")
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
	var decoded AgentConfig
	if len(wireValue.ID) != 0 {
		if err := decodeString(wireValue.ID, "id", &decoded.ID); err != nil {
			return err
		}
	}
	if len(wireValue.SessionID) != 0 {
		var identifier string
		if err := decodeString(
			wireValue.SessionID,
			"sessionId",
			&identifier,
		); err != nil {
			return err
		}
		if identifier == "" {
			return errors.New(
				"agentloop factory: Agent sessionId must be non-empty when present",
			)
		}
		decoded.SessionID = session.SessionID(identifier)
	}
	if len(wireValue.Provider) != 0 {
		if err := decodeString(
			wireValue.Provider,
			"provider",
			&decoded.Provider,
		); err != nil {
			return err
		}
	}
	if len(wireValue.Model) != 0 {
		if err := decodeString(
			wireValue.Model,
			"model",
			&decoded.Model,
		); err != nil {
			return err
		}
	}
	if len(wireValue.MaxTokens) != 0 {
		if isNull(wireValue.MaxTokens) {
			return errors.New(
				"agentloop factory: Agent maxTokens must be a positive safe integer",
			)
		}
		var tokenLimit int
		if err := json.Unmarshal(wireValue.MaxTokens, &tokenLimit); err != nil {
			return fmt.Errorf(
				"agentloop factory: Agent maxTokens must be a positive safe integer: %w",
				err,
			)
		}
		decoded.MaxTokens = &tokenLimit
	}
	if len(wireValue.CWD) != 0 {
		var workingDirectory string
		if err := decodeString(
			wireValue.CWD,
			"cwd",
			&workingDirectory,
		); err != nil {
			return err
		}
		decoded.CWD = &workingDirectory
	}
	if len(wireValue.ResumeSessionID) != 0 {
		var identifier string
		if err := decodeString(
			wireValue.ResumeSessionID,
			"resumeSessionId",
			&identifier,
		); err != nil {
			return err
		}
		decoded.ResumeSessionID = session.SessionID(identifier)
	}
	*entry = decoded
	return nil
}

func resolveSettings(settings Config) (agentloop.Settings, error) {
	parallelLimit := agentloop.DefaultMaxParallelToolCalls
	if settings.MaxParallelToolCalls != nil {
		parallelLimit = *settings.MaxParallelToolCalls
	}
	if parallelLimit < 1 {
		return agentloop.Settings{}, errors.New(
			"agentloop factory: maxParallelToolCalls must be a positive integer",
		)
	}
	declarations := make([]agentloop.StartupAgent, len(settings.Agents))
	// exactIdentities maps each configured Session ID to its catalog Agent ID.
	// Presence means a later catalog entry cannot claim the same Session.
	exactIdentities := make(map[session.SessionID]string)
	for index, configured := range settings.Agents {
		if strings.TrimSpace(configured.ID) == "" ||
			configured.ID != strings.TrimSpace(configured.ID) {
			return agentloop.Settings{}, fmt.Errorf(
				"agentloop factory: agents[%d].id must be non-empty and trimmed",
				index,
			)
		}
		if configured.SessionID != "" && configured.ResumeSessionID != "" {
			return agentloop.Settings{}, fmt.Errorf(
				"agentloop factory: Agent %q sessionId and resumeSessionId are mutually exclusive",
				configured.ID,
			)
		}
		if configured.MaxTokens != nil &&
			(*configured.MaxTokens <= 0 ||
				int64(*configured.MaxTokens) > int64(1<<53-1)) {
			return agentloop.Settings{}, fmt.Errorf(
				"agentloop factory: Agent %q maxTokens must be a positive safe integer",
				configured.ID,
			)
		}
		if configured.CWD != nil && !filepath.IsAbs(*configured.CWD) {
			return agentloop.Settings{}, fmt.Errorf(
				"agentloop factory: Agent %q cwd must be an absolute path",
				configured.ID,
			)
		}
		exactIdentity := configured.SessionID
		resume := false
		if configured.ResumeSessionID != "" {
			exactIdentity = configured.ResumeSessionID
			resume = true
		}
		if exactIdentity != "" {
			if firstLabel, exists := exactIdentities[exactIdentity]; exists {
				return agentloop.Settings{}, fmt.Errorf(
					"agentloop factory: Agents %q and %q use duplicate exact Session identity %q",
					firstLabel,
					configured.ID,
					exactIdentity,
				)
			}
			exactIdentities[exactIdentity] = configured.ID
		}
		metadata := session.Metadata{}
		if configured.CWD != nil {
			workingDirectory := *configured.CWD
			metadata.CWD = &workingDirectory
		}
		declarations[index] = agentloop.StartupAgent{
			Label:     configured.ID,
			SessionID: exactIdentity,
			Resume:    resume,
			AgentOptions: agent.Options{
				Provider:  configured.Provider,
				Model:     configured.Model,
				MaxTokens: cloneInt(configured.MaxTokens),
			},
			Metadata: metadata,
		}
	}
	return agentloop.Settings{
		MaxParallelToolCalls: parallelLimit,
		StartupAgents:        declarations,
	}, nil
}

func decodeString(
	encoded json.RawMessage,
	fieldName string,
	destination *string,
) error {
	if isNull(encoded) {
		return fmt.Errorf(
			"agentloop factory: Agent %s must be a string",
			fieldName,
		)
	}
	if err := json.Unmarshal(encoded, destination); err != nil {
		return fmt.Errorf(
			"agentloop factory: Agent %s must be a string: %w",
			fieldName,
			err,
		)
	}
	return nil
}

func isNull(encoded json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(encoded), []byte("null"))
}

func cloneInt(source *int) *int {
	if source == nil {
		return nil
	}
	detached := *source
	return &detached
}
