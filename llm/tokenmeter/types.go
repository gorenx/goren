// Package tokenmeter defines replay-aware request pressure measurement shared
// by Compaction and other pressure-sensitive consumers.
package tokenmeter

import (
	"context"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
)

const (
	// PluginName is the canonical Harness Token Meter Plugin name.
	PluginName = "@deepseek-ai/dsh-token-meter"
	// ServiceName preserves the canonical Cordis capability name.
	ServiceName       = "tokenMeter"
	maxSafeTokenCount = int64(1<<53 - 1)
)

// BaselineKind identifies the anchor used for current request pressure.
type BaselineKind string

const (
	BaselineNone      BaselineKind = "none"
	BaselineEstimated BaselineKind = "estimated"
	BaselineUsage     BaselineKind = "usage"
)

// Baseline is the provider or heuristic anchor of one measurement.
type Baseline struct {
	Kind   BaselineKind    `json:"kind"`
	Tokens int64           `json:"tokens"`
	Usage  *llm.TokenUsage `json:"usage,omitempty"`
}

// SurfaceNode prices one current Surface node in positional order.
type SurfaceNode struct {
	Seq    int64 `json:"seq"`
	Tokens int64 `json:"tokens"`
}

// Measurement is an immutable snapshot at one consumed Session log revision.
type Measurement struct {
	LogRevision        int64         `json:"logRevision"`
	Baseline           Baseline      `json:"baseline"`
	SurfaceDeltaTokens int64         `json:"surfaceDeltaTokens"`
	TotalTokens        int64         `json:"totalTokens"`
	SurfaceTokens      int64         `json:"surfaceTokens"`
	Nodes              []SurfaceNode `json:"nodes"`
}

// TokenUsageProjection is cumulative provider-reported usage over one log.
type TokenUsageProjection struct {
	UncachedInputTokens int64 `json:"uncachedInputTokens"`
	OutputTokens        int64 `json:"outputTokens"`
	CacheReadTokens     int64 `json:"cacheReadTokens"`
	CacheWriteTokens    int64 `json:"cacheWriteTokens"`
}

// ContextPressureProjection is a display-only last-wins occupancy view.
type ContextPressureProjection struct {
	PressureTokens  *int64 `json:"pressureTokens,omitempty"`
	ProjectedTokens *int64 `json:"projectedTokens,omitempty"`
	ContextWindow   *int64 `json:"contextWindow,omitempty"`
}

// ContextBreakdownProjection is the fixed-estimator request composition view.
type ContextBreakdownProjection struct {
	SystemTokens  int64 `json:"systemTokens"`
	ToolsTokens   int64 `json:"toolsTokens"`
	MessageTokens int64 `json:"messageTokens"`
}

// Meter is the singleton replay measurement Service.
type Meter interface {
	plugin.Service
	Measure(context.Context, session.Context, *session.EpochHeader) (Measurement, error)
	EstimateMessage(agentmessage.Message) (int64, error)
}
