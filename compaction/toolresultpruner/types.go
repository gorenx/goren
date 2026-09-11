// Package toolresultpruner defines the optional replay-safe model-free
// companion consumed by Basic Compaction.
package toolresultpruner

import (
	"github.com/gorenx/goren/agentmessage"
)

const (
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
	CallID         agentmessage.CallID
	CharsBefore    int
	CharsAfter     int
}

// Result aggregates one stable-snapshot pruning pass.
type Result struct {
	Pruned       []Entry
	CharsRemoved int
}

// Estimator supplies pure token estimation, never Session I/O or Actor requests.
type Estimator interface {
	EstimateMessage(agentmessage.Message) (int64, error)
}
