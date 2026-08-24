package tokenmeter

import (
	"encoding/json"
	"errors"

	"github.com/gorenx/goren/session"
	sessionprojection "github.com/gorenx/goren/session/projection"
)

// ContextBreakdownProjectionKey is the canonical Session projection key.
const ContextBreakdownProjectionKey = "contextBreakdown"

type contextBreakdownState struct {
	SystemTokens  int64             `json:"systemTokens"`
	ToolsTokens   int64             `json:"toolsTokens"`
	MessageTokens int64             `json:"messageTokens"`
	Claim         *shadowPriceClaim `json:"claim,omitempty"`
}

type contextBreakdownUnit struct{}

func (contextBreakdownUnit) Key() string { return ContextBreakdownProjectionKey }

func (contextBreakdownUnit) StateVersion() int64 { return 2 }

func (contextBreakdownUnit) InitialState() (json.RawMessage, error) {
	return encodeProjectionState(contextBreakdownState{})
}

func (contextBreakdownUnit) ApplyState(
	rawState json.RawMessage,
	entry session.Event,
) (sessionprojection.Transition, error) {
	var current contextBreakdownState
	if err := decodeProjectionState(rawState, &current); err != nil {
		return sessionprojection.Transition{}, err
	}
	if err := validateBreakdownState(current); err != nil {
		return sessionprojection.Transition{}, err
	}
	fold, err := foldProjectionSurface(current.Claim, entry)
	if err != nil {
		return sessionprojection.Transition{}, err
	}
	next := current
	next.Claim = cloneClaim(fold.claim)
	if entry.Type == session.RequestHeaderEventName {
		var snapshot session.RequestHeaderSnapshot
		if err := decodePayload(entry, &snapshot); err != nil {
			return sessionprojection.Transition{}, err
		}
		canonical := session.CanonicalEpochHeader(snapshot.Header)
		next.SystemTokens = estimateSystemTokens(&canonical)
		next.ToolsTokens, err = estimateToolsTokens(&canonical)
		if err != nil {
			return sessionprojection.Transition{}, err
		}
	}
	next.MessageTokens, err = applySurfaceDelta(current.MessageTokens, fold.delta)
	if err != nil {
		return sessionprojection.Transition{}, err
	}
	return projectionTransition(rawState, next)
}

func (contextBreakdownUnit) ViewState(rawState json.RawMessage) (json.RawMessage, error) {
	var current contextBreakdownState
	if err := decodeProjectionState(rawState, &current); err != nil {
		return nil, err
	}
	if err := validateBreakdownState(current); err != nil {
		return nil, err
	}
	return encodeProjectionState(ContextBreakdownProjection{
		SystemTokens:  current.SystemTokens,
		ToolsTokens:   current.ToolsTokens,
		MessageTokens: current.MessageTokens,
	})
}

func validateBreakdownState(current contextBreakdownState) error {
	values := []int64{
		current.SystemTokens,
		current.ToolsTokens,
		current.MessageTokens,
	}
	for _, value := range values {
		if value < 0 || value > maxSafeTokenCount {
			return errors.New("tokenmeter: invalid context breakdown projection state")
		}
	}
	return validateClaim(current.Claim)
}

var _ sessionprojection.Unit = contextBreakdownUnit{}
