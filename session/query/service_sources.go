package query

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/gorenx/goren/session"
)

// Observe atomically replaces the complete source observation. Live logs win
// over matching persisted logs; callers cannot mutate retained snapshots.
func (owner *Service) Observe(sources []Source) error {
	selected := make(map[session.SessionID]Source, len(sources))
	for _, candidate := range sources {
		if !candidate.Live && !candidate.Persisted {
			return errors.New("session query: source availability is absent")
		}
		identifier := candidate.Log.Header.ID
		existing, found := selected[identifier]
		if found {
			if !headersEqual(existing.Log.Header, candidate.Log.Header) {
				return sourceConflict(existing.Log.Header, candidate.Log.Header)
			}
			if (existing.Live && candidate.Live) || (existing.Persisted && candidate.Persisted) {
				return failure(ErrorSourceConflict, "session query: duplicate source availability", nil)
			}
			if existing.Live {
				existing.Persisted = existing.Persisted || candidate.Persisted
				selected[identifier] = existing
				continue
			}
			candidate.Persisted = candidate.Persisted || existing.Persisted
		}
		selected[identifier] = Source{
			Log: LogSnapshot{
				Header: cloneHeader(candidate.Log.Header),
				Events: cloneEvents(candidate.Log.Events),
			},
			Live:      candidate.Live,
			Persisted: candidate.Persisted,
		}
	}
	for _, source := range selected {
		if err := validateLogical(source.Log.Header, source.Log.Events); err != nil {
			return failure(ErrorCorruptSession, "session query: invalid observed log", err)
		}
	}
	owner.sources = selected
	return nil
}

func sourceRevision(snapshot LogSnapshot) (string, error) {
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}
