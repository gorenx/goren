package subagent

import (
	"errors"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
)

// CoordinatorSource attributes a parent coordinator's relay to a child.
type CoordinatorSource struct {
	Kind            string                   `json:"kind"`
	Form            agentmessage.ContextForm `json:"form"`
	SenderSessionID session.SessionID        `json:"senderSessionId"`
}

// SourceKind returns the canonical coordinator discriminant.
func (CoordinatorSource) SourceKind() string {
	return "coordinator"
}

// CloneSource validates and detaches coordinator attribution.
func (origin CoordinatorSource) CloneSource() (agentmessage.MessageSource, error) {
	if origin.SenderSessionID == "" {
		return nil, errors.New(
			"subagent: coordinator message source needs a senderSessionId",
		)
	}
	if origin.Form != "" && origin.Form != agentmessage.ContextRelay {
		return nil, errors.New(
			"subagent: coordinator message source form must be relay",
		)
	}
	origin.Kind = "coordinator"
	origin.Form = agentmessage.ContextRelay
	return origin, nil
}

// ReportSource attributes content explicitly selected by one
// continuable child for its direct parent.
type ReportSource struct {
	Kind            string                   `json:"kind"`
	Form            agentmessage.ContextForm `json:"form"`
	SenderSessionID session.SessionID        `json:"senderSessionId"`
}

// SourceKind returns the canonical subagent-report discriminant.
func (ReportSource) SourceKind() string {
	return "subagent-report"
}

// CloneSource validates and detaches report attribution.
func (origin ReportSource) CloneSource() (agentmessage.MessageSource, error) {
	if origin.SenderSessionID == "" {
		return nil, errors.New(
			"subagent: report message source needs a senderSessionId",
		)
	}
	if origin.Form != "" && origin.Form != agentmessage.ContextRelay {
		return nil, errors.New(
			"subagent: report message source form must be relay",
		)
	}
	origin.Kind = "subagent-report"
	origin.Form = agentmessage.ContextRelay
	return origin, nil
}

// SettlementSource attributes Runtime-authored settlement notice content
// without miscrediting it to the child.
type SettlementSource struct {
	Kind            string                   `json:"kind"`
	Form            agentmessage.ContextForm `json:"form"`
	Summary         string                   `json:"summary"`
	SenderSessionID session.SessionID        `json:"senderSessionId"`
}

// SourceKind returns the canonical subagent-settled discriminant.
func (SettlementSource) SourceKind() string {
	return "subagent-settled"
}

// CloneSource validates, bounds, and detaches settlement attribution.
func (origin SettlementSource) CloneSource() (agentmessage.MessageSource, error) {
	if origin.SenderSessionID == "" {
		return nil, errors.New(
			"subagent: settled message source needs a senderSessionId",
		)
	}
	if origin.Form != "" && origin.Form != agentmessage.ContextNotice {
		return nil, errors.New(
			"subagent: settled message source form must be notice",
		)
	}
	origin.Kind = "subagent-settled"
	origin.Form = agentmessage.ContextNotice
	origin.Summary = agentmessage.BoundContextSummary(origin.Summary)
	return origin, nil
}

var _ agentmessage.MessageSource = CoordinatorSource{}
var _ agentmessage.MessageSource = ReportSource{}
var _ agentmessage.MessageSource = SettlementSource{}
