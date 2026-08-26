// Package childrequest owns validation and snapshotting shared by Subagent
// implementations before SeedBuilder or Agent work begins.
package childrequest

import (
	"encoding/json"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/tools"
)

const maxSafeInteger int64 = 1<<53 - 1

// Snapshot validates and detaches caller-owned child inputs.
func Snapshot(source subagent.ChildRequest) (subagent.ChildRequest, error) {
	if source.MaxDepth != nil &&
		(*source.MaxDepth < 0 || *source.MaxDepth > maxSafeInteger) {
		return subagent.ChildRequest{}, errors.New(
			"subagent: maxDepth must be a non-negative safe integer",
		)
	}
	promptSnapshot, cloneErr := llm.CloneContentBlocks(source.Prompt)
	if cloneErr != nil {
		return subagent.ChildRequest{}, cloneErr
	}
	filterSnapshot, snapshotErr := snapshotRestriction(source.ToolFilter)
	if snapshotErr != nil {
		return subagent.ChildRequest{}, snapshotErr
	}
	return subagent.ChildRequest{
		Prompt:       promptSnapshot,
		Parent:       source.Parent,
		AgentOptions: snapshotAgentOptions(source.AgentOptions),
		MaxDepth:     cloneInt64(source.MaxDepth),
		ToolFilter:   filterSnapshot,
		Persona:      cloneString(source.Persona),
		OutputSchema: append(json.RawMessage(nil), source.OutputSchema...),
	}, nil
}

func snapshotAgentOptions(source *agent.Options) *agent.Options {
	if source == nil {
		return nil
	}
	detached := *source
	if source.MaxTokens != nil {
		maxTokensValue := *source.MaxTokens
		detached.MaxTokens = &maxTokensValue
	}
	return &detached
}

func snapshotRestriction(
	filterValue *tools.ToolRestriction,
) (*tools.ToolRestriction, error) {
	if filterValue == nil {
		return nil, nil
	}
	if filterValue.Allow == nil && filterValue.Deny == nil {
		return nil, errors.New(
			"subagent: toolFilter must declare allow and/or deny",
		)
	}
	return &tools.ToolRestriction{
		Allow: cloneStrings(filterValue.Allow),
		Deny:  cloneStrings(filterValue.Deny),
	}, nil
}

func cloneStrings(source []string) []string {
	if source == nil {
		return nil
	}
	detached := make([]string, len(source))
	copy(detached, source)
	return detached
}

func cloneInt64(source *int64) *int64 {
	if source == nil {
		return nil
	}
	detached := *source
	return &detached
}

func cloneString(source *string) *string {
	if source == nil {
		return nil
	}
	detached := *source
	return &detached
}
