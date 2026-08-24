package compaction

import (
	"encoding/json"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

const (
	StartEventName   = "compaction/start"
	SummaryEventName = "compaction/summary"
	EndEventName     = "compaction/end"
	PruneEventName   = "compaction/prune"
)

// Start acquires the durable compaction lock. A nil Turn is a manual attempt.
type Start struct {
	CompactionID    ID      `json:"compactionId"`
	SourceCommandID *string `json:"sourceCommandId,omitempty"`
	Turn            *int64  `json:"turn"`
}

// Summary records safe output, shadow pricing, provenance, and model call facts.
type Summary struct {
	CompactionID       ID                 `json:"compactionId"`
	SourceCommandID    *string            `json:"sourceCommandId,omitempty"`
	Summary            []llm.ContentBlock `json:"summary"`
	RawOutput          []llm.ContentBlock `json:"rawOutput,omitempty"`
	LLMStreamCall      bool               `json:"llmStreamCall,omitempty"`
	ShadowedRange      SurfaceRange       `json:"shadowedRange"`
	ShadowedSeqs       []int64            `json:"shadowedSeqs"`
	ShadowedTokenCount int64              `json:"shadowedTokenCount"`
	Provider           string             `json:"provider"`
	Model              string             `json:"model"`
	MaxTokens          *int               `json:"maxTokens,omitempty"`
	Usage              *llm.TokenUsage    `json:"usage,omitempty"`
}

// MarshalJSON preserves the merge-extended rawOutput/llmStreamCall union.
// A marked LLM call must retain rawOutput even when that output is empty.
func (entry Summary) MarshalJSON() ([]byte, error) {
	type wireSummary struct {
		CompactionID       ID                  `json:"compactionId"`
		SourceCommandID    *string             `json:"sourceCommandId,omitempty"`
		Summary            []llm.ContentBlock  `json:"summary"`
		RawOutput          *[]llm.ContentBlock `json:"rawOutput,omitempty"`
		LLMStreamCall      *bool               `json:"llmStreamCall,omitempty"`
		ShadowedRange      SurfaceRange        `json:"shadowedRange"`
		ShadowedSeqs       []int64             `json:"shadowedSeqs"`
		ShadowedTokenCount int64               `json:"shadowedTokenCount"`
		Provider           string              `json:"provider"`
		Model              string              `json:"model"`
		MaxTokens          *int                `json:"maxTokens,omitempty"`
		Usage              *llm.TokenUsage     `json:"usage,omitempty"`
	}

	var rawOutput *[]llm.ContentBlock
	if entry.RawOutput != nil || entry.LLMStreamCall {
		detached := append([]llm.ContentBlock(nil), entry.RawOutput...)
		if detached == nil {
			detached = []llm.ContentBlock{}
		}
		rawOutput = &detached
	}
	var llmStreamCall *bool
	if entry.LLMStreamCall {
		marked := true
		llmStreamCall = &marked
	}
	return json.Marshal(wireSummary{
		CompactionID:       entry.CompactionID,
		SourceCommandID:    entry.SourceCommandID,
		Summary:            entry.Summary,
		RawOutput:          rawOutput,
		LLMStreamCall:      llmStreamCall,
		ShadowedRange:      entry.ShadowedRange,
		ShadowedSeqs:       entry.ShadowedSeqs,
		ShadowedTokenCount: entry.ShadowedTokenCount,
		Provider:           entry.Provider,
		Model:              entry.Model,
		MaxTokens:          entry.MaxTokens,
		Usage:              entry.Usage,
	})
}

// End releases the durable lock and optionally records an unsuccessful attempt.
type End struct {
	CompactionID    ID      `json:"compactionId"`
	SourceCommandID *string `json:"sourceCommandId,omitempty"`
	Turn            *int64  `json:"turn"`
	Error           *string `json:"error,omitempty"`
}

// Prune prices the exact Surface node replaced by the immediately following
// tool/result Event.
type Prune struct {
	ShadowedRange      SurfaceRange `json:"shadowedRange"`
	ShadowedSeqs       []int64      `json:"shadowedSeqs"`
	ShadowedTokenCount int64        `json:"shadowedTokenCount"`
}

var (
	StartEvent   = session.DefineEvent[Start](StartEventName)
	SummaryEvent = session.DefineEvent[Summary](SummaryEventName)
	EndEvent     = session.DefineEvent[End](EndEventName)
	PruneEvent   = session.DefineEvent[Prune](PruneEventName)
)
