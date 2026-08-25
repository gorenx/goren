package continuation

import (
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/tools"
)

func snapshotRequest(
	source subagent.ContinuableRequest,
) (subagent.ContinuableRequest, error) {
	promptSnapshot, cloneErr := llm.CloneContentBlocks(source.Prompt)
	if cloneErr != nil {
		return subagent.ContinuableRequest{}, cloneErr
	}
	filterSnapshot, snapshotErr := snapshotRestriction(source.ToolFilter)
	if snapshotErr != nil {
		return subagent.ContinuableRequest{}, snapshotErr
	}
	return subagent.ContinuableRequest{
		Prompt:       promptSnapshot,
		Parent:       source.Parent,
		AgentOptions: snapshotAgentOptions(source.AgentOptions),
		MaxDepth:     cloneInt64(source.MaxDepth),
		ToolFilter:   filterSnapshot,
		Persona:      cloneString(source.Persona),
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
	snapshot := *source
	return &snapshot
}

func cloneString(source *string) *string {
	if source == nil {
		return nil
	}
	snapshot := *source
	return &snapshot
}
