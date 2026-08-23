package session

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gorenx/goren/llm"
)

// MarshalJSON validates and preserves the closed turn-ending union.
func (ending TurnEnd) MarshalJSON() ([]byte, error) {
	if !isSafeNonNegative(ending.Turn) || ending.Turn == 0 {
		return nil, fmt.Errorf(
			"session: turn/end turn must be a positive safe integer, got %d",
			ending.Turn,
		)
	}
	if ending.Reason == nil {
		return nil, errors.New("session: turn/end reason is required")
	}
	return json.Marshal(struct {
		Turn   int64         `json:"turn"`
		Reason TurnEndReason `json:"reason"`
	}{
		Turn:   ending.Turn,
		Reason: ending.Reason,
	})
}

// UnmarshalJSON restores the closed turn-ending union owned by Session.
func (ending *TurnEnd) UnmarshalJSON(rawValue []byte) error {
	if ending == nil {
		return errors.New("session: cannot decode turn/end into nil target")
	}
	var wireValue struct {
		Turn   int64           `json:"turn"`
		Reason json.RawMessage `json:"reason"`
	}
	if err := decodeSessionPayload(rawValue, &wireValue); err != nil {
		return fmt.Errorf("session: invalid turn/end: %w", err)
	}
	if !isSafeNonNegative(wireValue.Turn) || wireValue.Turn == 0 {
		return fmt.Errorf(
			"session: turn/end turn must be a positive safe integer, got %d",
			wireValue.Turn,
		)
	}
	decodedReason, err := decodeTurnEndReason(wireValue.Reason)
	if err != nil {
		return err
	}
	*ending = TurnEnd{
		Turn:   wireValue.Turn,
		Reason: decodedReason,
	}
	return nil
}

func decodeTurnEndReason(rawValue json.RawMessage) (TurnEndReason, error) {
	var discriminator struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(rawValue, &discriminator); err != nil {
		return nil, fmt.Errorf("session: invalid turn/end reason: %w", err)
	}
	switch discriminator.Kind {
	case "completed":
		if err := decodeReasonKind(rawValue, "completed"); err != nil {
			return nil, err
		}
		return TurnCompleted{}, nil
	case "blocked":
		if err := decodeReasonKind(rawValue, "blocked"); err != nil {
			return nil, err
		}
		return TurnBlocked{}, nil
	case "max-tokens":
		if err := decodeReasonKind(rawValue, "max-tokens"); err != nil {
			return nil, err
		}
		return TurnMaxTokens{}, nil
	case "interrupted":
		if err := decodeReasonKind(rawValue, "interrupted"); err != nil {
			return nil, err
		}
		return TurnInterrupted{}, nil
	case "aborted":
		return decodeTurnAborted(rawValue)
	case "error":
		var wireValue struct {
			Kind  string         `json:"kind"`
			Error llm.LlmFailure `json:"error"`
		}
		if err := decodeSessionPayload(rawValue, &wireValue); err != nil {
			return nil, fmt.Errorf("session: invalid error turn ending: %w", err)
		}
		decoded := TurnError{
			Error: wireValue.Error,
		}
		if _, err := decoded.MarshalJSON(); err != nil {
			return nil, err
		}
		return decoded, nil
	default:
		return nil, fmt.Errorf(
			"session: unsupported turn/end reason %q",
			discriminator.Kind,
		)
	}
}

func decodeReasonKind(rawValue json.RawMessage, expected string) error {
	var wireValue struct {
		Kind string `json:"kind"`
	}
	if err := decodeSessionPayload(rawValue, &wireValue); err != nil {
		return fmt.Errorf("session: invalid %s ending: %w", expected, err)
	}
	if wireValue.Kind != expected {
		return fmt.Errorf(
			"session: ending kind is %q, want %q",
			wireValue.Kind,
			expected,
		)
	}
	return nil
}

func decodeTurnAborted(rawValue json.RawMessage) (TurnEndReason, error) {
	var wireValue struct {
		Kind   string          `json:"kind"`
		Reason json.RawMessage `json:"reason"`
	}
	if err := decodeSessionPayload(rawValue, &wireValue); err != nil {
		return nil, fmt.Errorf("session: invalid aborted turn ending: %w", err)
	}
	var discriminator struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(wireValue.Reason, &discriminator); err != nil {
		return nil, fmt.Errorf("session: invalid turn cancellation reason: %w", err)
	}
	var cause TurnCancelCause
	switch discriminator.Kind {
	case "user":
		if err := decodeReasonKind(wireValue.Reason, "user"); err != nil {
			return nil, err
		}
		cause = UserCancelCause{}
	case "parent":
		if err := decodeReasonKind(wireValue.Reason, "parent"); err != nil {
			return nil, err
		}
		cause = ParentCancelCause{}
	case "disposed":
		if err := decodeReasonKind(wireValue.Reason, "disposed"); err != nil {
			return nil, err
		}
		cause = DisposedCancelCause{}
	case "hook":
		var hookCause struct {
			Kind   string `json:"kind"`
			Reason string `json:"reason"`
		}
		if err := decodeSessionPayload(wireValue.Reason, &hookCause); err != nil {
			return nil, fmt.Errorf("session: invalid hook cancellation reason: %w", err)
		}
		if hookCause.Reason == "" {
			return nil, errors.New("session: hook cancellation reason must be non-empty")
		}
		cause = HookCancelCause{
			Reason: hookCause.Reason,
		}
	default:
		return nil, fmt.Errorf(
			"session: unsupported turn cancellation reason %q",
			discriminator.Kind,
		)
	}
	return TurnAborted{
		Reason: cause,
	}, nil
}
