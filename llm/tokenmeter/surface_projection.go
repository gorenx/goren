package tokenmeter

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gorenx/goren/compaction"
	"github.com/gorenx/goren/session"
)

type shadowPriceClaim struct {
	Start  int64 `json:"start"`
	End    int64 `json:"end"`
	Tokens int64 `json:"tokens"`
}

type surfaceProjectionFold struct {
	delta int64
	claim *shadowPriceClaim
}

func foldProjectionSurface(
	claimValue *shadowPriceClaim,
	entry session.Event,
) (surfaceProjectionFold, error) {
	if entry.Type == compaction.SummaryEventName || entry.Type == compaction.PruneEventName {
		nextClaim, err := shadowPriceFromEvent(entry)
		if err != nil {
			return surfaceProjectionFold{}, err
		}
		return surfaceProjectionFold{
			claim: nextClaim,
		}, nil
	}
	if entry.SurfaceOp == nil {
		return surfaceProjectionFold{}, nil
	}
	messageValue, err := session.DeriveEventMessage(entry)
	if err != nil {
		return surfaceProjectionFold{}, err
	}
	eventTokens := int64(0)
	if messageValue != nil {
		eventTokens, err = estimateMessage(messageValue)
		if err != nil {
			return surfaceProjectionFold{}, err
		}
	}
	switch entry.SurfaceOp.Kind {
	case session.SurfaceOperationAppend:
		return surfaceProjectionFold{
			delta: eventTokens,
		}, nil
	case session.SurfaceOperationReplace:
		if claimValue == nil {
			return surfaceProjectionFold{}, nil
		}
		if claimValue.Start != entry.SurfaceOp.Start || claimValue.End != entry.SurfaceOp.End {
			return surfaceProjectionFold{}, fmt.Errorf(
				"token surface: replace at seq %d over range %d-%d has no adjacent shadow price (armed claim covers %d-%d)",
				entry.Seq,
				entry.SurfaceOp.Start,
				entry.SurfaceOp.End,
				claimValue.Start,
				claimValue.End,
			)
		}
		return surfaceProjectionFold{
			delta: eventTokens - claimValue.Tokens,
		}, nil
	default:
		return surfaceProjectionFold{}, fmt.Errorf(
			"tokenmeter: unsupported surface operation %q",
			entry.SurfaceOp.Kind,
		)
	}
}

func shadowPriceFromEvent(entry session.Event) (*shadowPriceClaim, error) {
	var wireValue struct {
		ShadowedRange      compaction.SurfaceRange `json:"shadowedRange"`
		ShadowedTokenCount int64                   `json:"shadowedTokenCount"`
	}
	if err := json.Unmarshal(entry.Data, &wireValue); err != nil {
		return nil, err
	}
	if wireValue.ShadowedRange.Start < 0 ||
		wireValue.ShadowedRange.End < 0 ||
		wireValue.ShadowedTokenCount < 0 ||
		wireValue.ShadowedTokenCount > maxSafeTokenCount {
		return nil, fmt.Errorf("tokenmeter: invalid shadow price at seq %d", entry.Seq)
	}
	return &shadowPriceClaim{
		Start:  wireValue.ShadowedRange.Start,
		End:    wireValue.ShadowedRange.End,
		Tokens: wireValue.ShadowedTokenCount,
	}, nil
}

func applySurfaceDelta(base int64, delta int64) (int64, error) {
	if base < 0 || base > maxSafeTokenCount {
		return 0, errors.New("tokenmeter: surface token count is outside safe range")
	}
	if delta >= 0 {
		return addTokens(base, delta)
	}
	if delta < -maxSafeTokenCount || base < -delta {
		return 0, errors.New("tokenmeter: surface token delta makes the total negative")
	}
	return base + delta, nil
}

func validateClaim(claimValue *shadowPriceClaim) error {
	if claimValue == nil {
		return nil
	}
	if claimValue.Start < 0 || claimValue.End < 0 ||
		claimValue.Tokens < 0 || claimValue.Tokens > maxSafeTokenCount {
		return errors.New("tokenmeter: invalid shadow price claim")
	}
	return nil
}

func cloneClaim(source *shadowPriceClaim) *shadowPriceClaim {
	if source == nil {
		return nil
	}
	detached := *source
	return &detached
}
