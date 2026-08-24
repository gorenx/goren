// Package toolresultpruner defines the optional replay-safe model-free
// companion consumed by Basic Compaction.
package toolresultpruner

import (
	"context"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
)

const (
	// PluginName is the canonical Harness Tool Result Pruner Plugin name.
	PluginName = "@deepseek-ai/dsh-compaction-tool-result-pruner"
	// ServiceName preserves the canonical Cordis capability name.
	ServiceName = "toolResultPruner"
	// PruneMarker replaces the omitted middle of an oversized result.
	PruneMarker = "\n\n[... tool result middle pruned ...]\n\n"
)

// Config defines deterministic Unicode code-point budgets.
type Config struct {
	ThresholdChars *int `json:"thresholdChars,omitempty"`
	HeadChars      *int `json:"headChars,omitempty"`
	TailChars      *int `json:"tailChars,omitempty"`
}

// ResolvedConfig is the validated construction snapshot.
type ResolvedConfig struct {
	ThresholdChars int
	HeadChars      int
	TailChars      int
}

// Entry records one landed tool/result Surface replacement.
type Entry struct {
	OriginalSeq    int64
	ReplacementSeq int64
	CallID         llm.CallID
	CharsBefore    int
	CharsAfter     int
}

// Result aggregates one stable-snapshot pruning pass.
type Result struct {
	Pruned       []Entry
	CharsRemoved int
}

// Pruner is the optional model-free companion Service.
type Pruner interface {
	plugin.Service
	MeasureContent([]llm.ContentBlock) (int, error)
	PruneContent([]llm.ContentBlock) ([]llm.ContentBlock, bool, error)
	PruneSession(context.Context, *session.Session) (Result, error)
}
