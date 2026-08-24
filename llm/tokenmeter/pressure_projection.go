package tokenmeter

import (
	"encoding/json"
	"errors"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	sessionprojection "github.com/gorenx/goren/session/projection"
)

// ContextPressureProjectionKey is the canonical Session projection key.
const ContextPressureProjectionKey = "contextPressure"

type contextPressureState struct {
	ContextWindow        *int64            `json:"contextWindow,omitempty"`
	PressureTokens       *int64            `json:"pressureTokens,omitempty"`
	SurfaceTokens        int64             `json:"surfaceTokens"`
	SampledSurfaceTokens *int64            `json:"sampledSurfaceTokens,omitempty"`
	Claim                *shadowPriceClaim `json:"claim,omitempty"`
}

type contextPressureUnit struct{}

func (contextPressureUnit) Key() string { return ContextPressureProjectionKey }

func (contextPressureUnit) StateVersion() int64 { return 4 }

func (contextPressureUnit) InitialState() (json.RawMessage, error) {
	return encodeProjectionState(contextPressureState{})
}

func (contextPressureUnit) ApplyState(
	rawState json.RawMessage,
	entry session.Event,
) (sessionprojection.Transition, error) {
	var current contextPressureState
	if err := decodeProjectionState(rawState, &current); err != nil {
		return sessionprojection.Transition{}, err
	}
	if err := validateContextPressureState(current); err != nil {
		return sessionprojection.Transition{}, err
	}
	fold, err := foldProjectionSurface(current.Claim, entry)
	if err != nil {
		return sessionprojection.Transition{}, err
	}
	next := current
	next.ContextWindow = cloneInt64(current.ContextWindow)
	next.PressureTokens = cloneInt64(current.PressureTokens)
	next.SampledSurfaceTokens = cloneInt64(current.SampledSurfaceTokens)
	next.Claim = cloneClaim(fold.claim)

	if entry.Type == session.RequestContextEventName {
		var routeContext session.RequestRouteContext
		if err := decodePayload(entry, &routeContext); err != nil {
			return sessionprojection.Transition{}, err
		}
		if routeContext.ContextWindow == nil {
			next.ContextWindow = nil
		} else {
			capacity := int64(*routeContext.ContextWindow)
			if capacity <= 0 || capacity > maxSafeTokenCount {
				return sessionprojection.Transition{}, errors.New(
					"tokenmeter: context window must be a positive safe integer",
				)
			}
			next.ContextWindow = &capacity
		}
	}
	_, _, usageValue, found, err := usageSampleFromEvent(entry)
	if err != nil {
		return sessionprojection.Transition{}, err
	}
	if found {
		pressureTokens, pressureErr := promptPressureTokens(usageValue)
		if pressureErr != nil {
			return sessionprojection.Transition{}, pressureErr
		}
		next.PressureTokens = &pressureTokens
		sampled := current.SurfaceTokens
		next.SampledSurfaceTokens = &sampled
	}
	nextSurfaceTokens, err := applySurfaceDelta(current.SurfaceTokens, fold.delta)
	if err != nil {
		return sessionprojection.Transition{}, err
	}
	next.SurfaceTokens = nextSurfaceTokens
	return projectionTransition(rawState, next)
}

func (contextPressureUnit) ViewState(rawState json.RawMessage) (json.RawMessage, error) {
	var current contextPressureState
	if err := decodeProjectionState(rawState, &current); err != nil {
		return nil, err
	}
	if err := validateContextPressureState(current); err != nil {
		return nil, err
	}
	viewValue := ContextPressureProjection{
		PressureTokens: cloneInt64(current.PressureTokens),
		ContextWindow:  cloneInt64(current.ContextWindow),
	}
	if current.PressureTokens != nil && current.SampledSurfaceTokens != nil {
		delta := current.SurfaceTokens - *current.SampledSurfaceTokens
		projected, err := applySignedPressure(*current.PressureTokens, delta)
		if err != nil {
			return nil, err
		}
		viewValue.ProjectedTokens = &projected
	}
	return encodeProjectionState(viewValue)
}

func promptPressureTokens(usageValue llm.TokenUsage) (int64, error) {
	if err := validateUsage(usageValue); err != nil {
		return 0, err
	}
	total := usageValue.InputTokens
	var err error
	for _, value := range []*int64{
		usageValue.CacheReadTokens,
		usageValue.CacheWriteTokens,
	} {
		if value != nil {
			total, err = addTokens(total, *value)
			if err != nil {
				return 0, err
			}
		}
	}
	return total, nil
}

func applySignedPressure(base int64, delta int64) (int64, error) {
	if base < 0 || base > maxSafeTokenCount {
		return 0, errors.New("tokenmeter: pressure token count is outside safe range")
	}
	if delta >= 0 {
		return addTokens(base, delta)
	}
	if delta < -maxSafeTokenCount {
		return 0, errors.New("tokenmeter: projected pressure delta is outside safe range")
	}
	result := base + delta
	if result < 0 {
		return 0, nil
	}
	return result, nil
}

func validateContextPressureState(current contextPressureState) error {
	values := []*int64{
		current.ContextWindow,
		current.PressureTokens,
		&current.SurfaceTokens,
		current.SampledSurfaceTokens,
	}
	for _, value := range values {
		if value != nil && (*value < 0 || *value > maxSafeTokenCount) {
			return errors.New("tokenmeter: invalid context pressure projection state")
		}
	}
	return validateClaim(current.Claim)
}

var _ sessionprojection.Unit = contextPressureUnit{}
