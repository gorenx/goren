// Package agentloop owns concrete Agent construction, lifecycle, and the
// default turn/step/tool-call loop over durable Sessions.
package agentloop

import (
	"errors"

	"github.com/gorenx/goren/agent"
)

// RuntimeOptions contains process-local failure reporting policy.
type RuntimeOptions struct {
	ObserverError func(error)
}

const (
	// PluginName is the canonical Harness Agent Loop Plugin name.
	PluginName = "@deepseek-ai/dsh-agent-loop"
	// DefaultMaxParallelToolCalls is the source deployment-wide scheduler cap.
	DefaultMaxParallelToolCalls = 10
	// maxSafeInteger is the largest integer exactly representable by JSON clients.
	maxSafeInteger = int64(1<<53 - 1)
)

// Settings contains construction-time policy already decoded by a Factory.
type Settings struct {
	MaxParallelToolCalls int
}

func validateSettings(candidate Settings) (Settings, error) {
	if candidate.MaxParallelToolCalls < 1 {
		return Settings{}, errors.New(
			"agentloop: maxParallelToolCalls must be a positive integer",
		)
	}
	return candidate, nil
}

func validateAgentOptions(agentOptions agent.Options) error {
	if agentOptions.MaxTokens != nil &&
		(*agentOptions.MaxTokens <= 0 ||
			int64(*agentOptions.MaxTokens) > maxSafeInteger) {
		return errors.New(
			"agentloop: Agent maxTokens must be a positive safe integer",
		)
	}
	return nil
}

func cloneInt(source *int) *int {
	if source == nil {
		return nil
	}
	copyValue := *source
	return &copyValue
}
