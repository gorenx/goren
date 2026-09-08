package modelrequest

import (
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

// HeaderDraft records the first request of an activation or a changed effective
// header. A nil draft means the existing header already describes this request.
// first remains true until the returned draft has committed successfully.
func HeaderDraft(previous *session.EpochHeader, effective session.EpochHeader, first bool) (*session.EventDraft, error) {
	canonical := session.CanonicalEpochHeader(effective)
	reason := session.RequestHeaderChange
	if first {
		reason = session.RequestHeaderInitial
		if previous != nil {
			reason = session.RequestHeaderResume
		}
	} else if previous != nil && session.EpochHeaderEqual(*previous, canonical) {
		return nil, nil
	}
	draft, err := session.NewEventDraft(session.RequestHeaderSet, session.RequestHeaderSnapshot{
		Header: canonical,
		Reason: reason,
	})
	if err != nil {
		return nil, err
	}
	return &draft, nil
}

// ContextDraft records a changed route or context-window limit without I/O.
func ContextDraft(previous *session.RequestRouteContext, effective session.RequestRouteContext) (*session.EventDraft, error) {
	if previous != nil && sameContext(*previous, effective) {
		return nil, nil
	}
	draft, err := session.NewEventDraft(session.RequestContextSet, effective)
	if err != nil {
		return nil, err
	}
	return &draft, nil
}

// Proposal removes adapter-derived defaults so a subsequent adapter resolution
// can recompute them. The returned parameters do not alias the persisted header.
func Proposal(previous session.EpochHeader) llm.CallConfig {
	proposed := llm.CloneCallConfig(previous.Config)
	if previous.AdapterDefaults != nil {
		if previous.AdapterDefaults.ReasoningEffort {
			proposed.ReasoningEffort = ""
		}
		if previous.AdapterDefaults.MaxTokens {
			proposed.MaxTokens = nil
		}
	}
	return proposed
}

func sameContext(left session.RequestRouteContext, right session.RequestRouteContext) bool {
	if left.Provider != right.Provider || left.Model != right.Model {
		return false
	}
	if left.ContextWindow == nil || right.ContextWindow == nil {
		return left.ContextWindow == nil && right.ContextWindow == nil
	}
	return *left.ContextWindow == *right.ContextWindow
}
