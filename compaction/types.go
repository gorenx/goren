// Package compaction defines the provider-neutral Context Compaction
// capability, durable vocabulary, and transaction result contracts.
package compaction

import (
	"fmt"

	"github.com/gorenx/goren/agentmessage"
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

// Result records one successful durable compaction transaction.
type Result struct {
	CompactionID       ID
	SourceCommandID    *string
	StartSeq           int64
	SummarySeq         int64
	EndSeq             int64
	Summary            []agentmessage.ContentBlock
	ShadowedRange      SurfaceRange
	ShadowedSeqs       []int64
	ShadowedTokenCount int64
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
