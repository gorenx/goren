// Package agentloop owns concrete Agent construction, lifecycle, and the
// default turn/step/tool-call loop over durable Sessions.
package agentloop

import (
	"errors"
	"fmt"
	"strings"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
)

const (
	// PluginName is the canonical Harness Agent Loop Plugin name.
	PluginName = "@deepseek-ai/dsh-agent-loop"
	// DefaultMaxParallelToolCalls is the source deployment-wide scheduler cap.
	DefaultMaxParallelToolCalls = 10
	// maxSafeInteger is the largest integer exactly representable by JSON clients.
	maxSafeInteger = int64(1<<53 - 1)
)

// StartupAgent is one validated boot-time Agent declaration. It contains no
// wire-format concerns; the factory package maps strict configuration into it.
type StartupAgent struct {
	Label        string
	SessionID    session.SessionID
	Resume       bool
	AgentOptions agent.Options
	Metadata     session.Metadata
}

// Settings contains construction-time policy already decoded by a Factory.
type Settings struct {
	MaxParallelToolCalls int
	StartupAgents        []StartupAgent
}

func validateSettings(candidate Settings) (Settings, error) {
	if candidate.MaxParallelToolCalls < 1 {
		return Settings{}, errors.New(
			"agentloop: maxParallelToolCalls must be a positive integer",
		)
	}
	validated := Settings{
		MaxParallelToolCalls: candidate.MaxParallelToolCalls,
		StartupAgents:        cloneStartupAgents(candidate.StartupAgents),
	}
	// exactIdentities maps each explicitly configured Session ID to its Agent
	// label. Presence means another startup declaration cannot reuse that ID.
	exactIdentities := make(map[session.SessionID]string)
	for index := range validated.StartupAgents {
		declaration := &validated.StartupAgents[index]
		if strings.TrimSpace(declaration.Label) == "" ||
			declaration.Label != strings.TrimSpace(declaration.Label) {
			return Settings{}, fmt.Errorf(
				"agentloop: startup Agent %d label must be non-empty and trimmed",
				index,
			)
		}
		if declaration.Resume && declaration.SessionID == "" {
			return Settings{}, fmt.Errorf(
				"agentloop: startup Agent %q resume identity is empty",
				declaration.Label,
			)
		}
		if err := validateAgentOptions(declaration.AgentOptions); err != nil {
			return Settings{}, fmt.Errorf(
				"agentloop: startup Agent %q: %w",
				declaration.Label,
				err,
			)
		}
		if declaration.SessionID == "" {
			continue
		}
		if firstLabel, exists := exactIdentities[declaration.SessionID]; exists {
			return Settings{}, fmt.Errorf(
				"agentloop: startup Agents %q and %q use duplicate exact Session identity %q",
				firstLabel,
				declaration.Label,
				declaration.SessionID,
			)
		}
		exactIdentities[declaration.SessionID] = declaration.Label
	}
	return validated, nil
}

func cloneStartupAgents(declarations []StartupAgent) []StartupAgent {
	if declarations == nil {
		return nil
	}
	detached := make([]StartupAgent, len(declarations))
	for index, declaration := range declarations {
		detached[index] = declaration
		detached[index].AgentOptions = cloneAgentOptions(
			declaration.AgentOptions,
		)
		detached[index].Metadata = cloneSessionMetadata(declaration.Metadata)
	}
	return detached
}

func cloneSessionMetadata(source session.Metadata) session.Metadata {
	return session.Metadata{
		CreatedAt:       cloneInt64(source.CreatedAt),
		CWD:             cloneString(source.CWD),
		ParentSession:   cloneSessionID(source.ParentSession),
		SeedLength:      cloneInt64(source.SeedLength),
		Origin:          source.Origin,
		DelegationDepth: cloneInt64(source.DelegationDepth),
		AgentPreset:     cloneString(source.AgentPreset),
	}
}

func cloneInt(source *int) *int {
	if source == nil {
		return nil
	}
	copyValue := *source
	return &copyValue
}

func cloneInt64(source *int64) *int64 {
	if source == nil {
		return nil
	}
	copyValue := *source
	return &copyValue
}

func cloneString(source *string) *string {
	if source == nil {
		return nil
	}
	copyValue := *source
	return &copyValue
}

func cloneSessionID(source *session.SessionID) *session.SessionID {
	if source == nil {
		return nil
	}
	copyValue := *source
	return &copyValue
}
