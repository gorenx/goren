package subagent

import (
	"errors"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

// CoordinatorSource attributes a parent coordinator's relay to a child.
type CoordinatorSource struct {
	Kind            string            `json:"kind"`
	Form            llm.ContextForm   `json:"form"`
	SenderSessionID session.SessionID `json:"senderSessionId"`
}

// SourceKind returns the canonical coordinator discriminant.
func (CoordinatorSource) SourceKind() string {
	return "coordinator"
}

// CloneSource validates and detaches coordinator attribution.
func (origin CoordinatorSource) CloneSource() (llm.MessageSource, error) {
	if origin.SenderSessionID == "" {
		return nil, errors.New(
			"subagent: coordinator message source needs a senderSessionId",
		)
	}
	if origin.Form != "" && origin.Form != llm.ContextRelay {
		return nil, errors.New(
			"subagent: coordinator message source form must be relay",
		)
	}
	origin.Kind = "coordinator"
	origin.Form = llm.ContextRelay
	return origin, nil
}

// ReportSource attributes content explicitly selected by one
// continuable child for its direct parent.
type ReportSource struct {
	Kind            string            `json:"kind"`
	Form            llm.ContextForm   `json:"form"`
	SenderSessionID session.SessionID `json:"senderSessionId"`
}

// SourceKind returns the canonical subagent-report discriminant.
func (ReportSource) SourceKind() string {
	return "subagent-report"
}

// CloneSource validates and detaches report attribution.
func (origin ReportSource) CloneSource() (llm.MessageSource, error) {
	if origin.SenderSessionID == "" {
		return nil, errors.New(
			"subagent: report message source needs a senderSessionId",
		)
	}
	if origin.Form != "" && origin.Form != llm.ContextRelay {
		return nil, errors.New(
			"subagent: report message source form must be relay",
		)
	}
	origin.Kind = "subagent-report"
	origin.Form = llm.ContextRelay
	return origin, nil
}

// SettlementSource attributes Runtime-authored settlement notice content
// without miscrediting it to the child.
type SettlementSource struct {
	Kind            string            `json:"kind"`
	Form            llm.ContextForm   `json:"form"`
	Summary         string            `json:"summary"`
	SenderSessionID session.SessionID `json:"senderSessionId"`
}

// SourceKind returns the canonical subagent-settled discriminant.
func (SettlementSource) SourceKind() string {
	return "subagent-settled"
}

// CloneSource validates, bounds, and detaches settlement attribution.
func (origin SettlementSource) CloneSource() (llm.MessageSource, error) {
	if origin.SenderSessionID == "" {
		return nil, errors.New(
			"subagent: settled message source needs a senderSessionId",
		)
	}
	if origin.Form != "" && origin.Form != llm.ContextNotice {
		return nil, errors.New(
			"subagent: settled message source form must be notice",
		)
	}
	origin.Kind = "subagent-settled"
	origin.Form = llm.ContextNotice
	origin.Summary = llm.BoundContextSummary(origin.Summary)
	return origin, nil
}

var _ llm.MessageSource = CoordinatorSource{}
var _ llm.MessageSource = ReportSource{}
var _ llm.MessageSource = SettlementSource{}
