package subagent

import (
	"encoding/json"
	"errors"
	"fmt"

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

// Delivery identifies one complete parent turn relayed to a Bound child. When
// the child Inbox durably accepts the carrying message, this identity lets the
// worker reconcile that receipt after a crash.
type Delivery struct {
	Kind            string                   `json:"kind"`
	Form            agentmessage.ContextForm `json:"form"`
	ParentSessionID session.SessionID        `json:"parentSessionId"`
	Turn            int64                    `json:"turn"`
	FromSeq         int64                    `json:"fromSeq"`
	ThroughSeq      int64                    `json:"throughSeq"`
	Outcome         string                   `json:"outcome"`
}

// SourceKind returns the canonical delivery discriminant.
func (Delivery) SourceKind() string {
	return "subagent-delivery"
}

// CloneSource validates and detaches the delivery identity.
func (origin Delivery) CloneSource() (agentmessage.MessageSource, error) {
	if origin.ParentSessionID == "" {
		return nil, errors.New(
			"subagent: delivery needs a parentSessionId",
		)
	}
	if origin.Form != "" && origin.Form != agentmessage.ContextRelay {
		return nil, errors.New(
			"subagent: delivery form must be relay",
		)
	}
	if origin.Turn <= 0 || origin.Turn > maxSafeInteger ||
		origin.FromSeq < 0 || origin.FromSeq > maxSafeInteger ||
		origin.ThroughSeq < origin.FromSeq ||
		origin.ThroughSeq > maxSafeInteger {
		return nil, errors.New(
			"subagent: delivery has an invalid turn or sequence range",
		)
	}
	if !validDeliveryOutcome(origin.Outcome) {
		return nil, fmt.Errorf(
			"subagent: unsupported delivery outcome %q",
			origin.Outcome,
		)
	}
	origin.Kind = "subagent-delivery"
	origin.Form = agentmessage.ContextRelay
	return origin, nil
}

// DecodeDelivery restores a delivery from either its live concrete value or
// the opaque extension source retained by message replay.
func DecodeDelivery(
	origin agentmessage.MessageSource,
) (Delivery, error) {
	if origin == nil || origin.SourceKind() != "subagent-delivery" {
		return Delivery{}, errors.New(
			"subagent: message source is not a Bound delivery",
		)
	}
	rawValue, err := json.Marshal(origin)
	if err != nil {
		return Delivery{}, err
	}
	var decoded Delivery
	if err = decodeBoundJSON(rawValue, &decoded); err != nil {
		return Delivery{}, err
	}
	detached, err := decoded.CloneSource()
	if err != nil {
		return Delivery{}, err
	}
	return detached.(Delivery), nil
}

func validDeliveryOutcome(outcome string) bool {
	switch outcome {
	case "completed", "blocked", "max-tokens", "interrupted", "aborted", "error":
		return true
	default:
		return false
	}
}

var _ agentmessage.MessageSource = CoordinatorSource{}
var _ agentmessage.MessageSource = ReportSource{}
var _ agentmessage.MessageSource = SettlementSource{}
var _ agentmessage.MessageSource = Delivery{}
