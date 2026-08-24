// Package compaction defines the provider-neutral Context Compaction
// capability, durable vocabulary, and transaction result contracts.
package compaction

import (
	"context"
	"errors"
	"fmt"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
)

const (
	// PluginName is the canonical Harness Service Definition package name.
	PluginName = "@deepseek-ai/dsh-compaction"
	// ServiceName preserves the canonical Cordis capability name.
	ServiceName = "compaction"
)

// ID identifies one durable compaction transaction.
type ID string

// Trigger explains why automatic policy is considering compaction.
type Trigger string

const (
	// TriggerPressure applies normal threshold and retention policy.
	TriggerPressure Trigger = "pressure"
	// TriggerContextOverflow attempts recovery from a canonical provider overflow.
	TriggerContextOverflow Trigger = "context-overflow"
)

// ManualErrorCode classifies an expected explicit compaction failure.
type ManualErrorCode string

const (
	ManualErrorBusy        ManualErrorCode = "busy"
	ManualErrorCancelled   ManualErrorCode = "cancelled"
	ManualErrorChanged     ManualErrorCode = "changed"
	ManualErrorSummary     ManualErrorCode = "summary"
	ManualErrorCommit      ManualErrorCode = "commit"
	ManualErrorPersistence ManualErrorCode = "persistence"
)

// ManualError is stable enough for a future command Consumer to branch on.
type ManualError struct {
	Code    ManualErrorCode
	Message string
	Cause   error
}

// Error returns the owned diagnostic without exposing an adapter error type.
func (problem *ManualError) Error() string {
	if problem == nil {
		return "compaction: manual failure"
	}
	if problem.Message != "" {
		return problem.Message
	}
	return fmt.Sprintf("compaction: manual failure %q", problem.Code)
}

// Unwrap retains the original technical cause for errors.Is/errors.As.
func (problem *ManualError) Unwrap() error {
	if problem == nil {
		return nil
	}
	return problem.Cause
}

// SurfaceRange names an inclusive span by current Surface position. Start may
// be numerically greater than End after an older span was replaced.
type SurfaceRange struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

// AgentContext is the minimal provider-neutral Agent snapshot Compaction uses.
type AgentContext struct {
	Session  session.Context
	Provider string
	Model    string
}

// ManualAgentContext is the minimal live Agent view required by CompactNow.
// The real Agent capability satisfies it directly; Compaction does not require
// an adapter or reconstruct a second maintenance owner.
type ManualAgentContext interface {
	SessionValue() session.Context
	OptionsValue() agent.Options
	RunMaintenance(context.Context, func(context.Context) error) error
}

// Result records one successful durable compaction transaction.
type Result struct {
	CompactionID       ID
	SourceCommandID    *string
	StartSeq           int64
	SummarySeq         int64
	EndSeq             int64
	Summary            []llm.ContentBlock
	ShadowedRange      SurfaceRange
	ShadowedSeqs       []int64
	ShadowedTokenCount int64
}

// Engine is the Service Definition implemented by one Compaction Provider.
type Engine interface {
	plugin.Service
	CompactIfNeeded(context.Context, AgentContext, Trigger) (*Result, error)
	CompactNow(context.Context, ManualAgentContext, *string) (*Result, error)
	CompactRegion(context.Context, int64, int64, AgentContext) (Result, error)
}

// ValidateTrigger rejects values outside the closed source union.
func ValidateTrigger(selectedTrigger Trigger) error {
	switch selectedTrigger {
	case TriggerPressure, TriggerContextOverflow:
		return nil
	default:
		return fmt.Errorf("compaction: unsupported trigger %q", selectedTrigger)
	}
}

// ValidateAgentContext checks the stable Definition boundary before a Provider
// starts policy or asynchronous work.
func ValidateAgentContext(subject AgentContext) error {
	if subject.Session == nil {
		return errors.New("compaction: Agent Context needs a Session")
	}
	return nil
}
