package companion

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/gorenx/goren/session"
)

// ErrConflict rejects a changed management revision or conflicting retry.
var ErrConflict = errors.New("companion: management revision conflicts")

// ErrHistory rejects invalid or inconsistent persisted relationship facts.
var ErrHistory = errors.New("companion: invalid Companion history")

const maximumRevision int64 = 1<<53 - 1

// Companion is a named companion Agent owned by one parent Session.
// Its identity and management state survive individual Agent runs.
// Configuration names its independent Settings group. Configuration values,
// runtime lifetimes and addresses never belong in this relationship record.
type Companion struct {
	ParentSessionID session.SessionID `json:"parentSessionID"`
	Name            string            `json:"name"`
	ChildSessionID  session.SessionID `json:"childSessionID"`
	Revision        int64             `json:"revision"`
	SourceRevision  int64             `json:"sourceRevision"`
	Configuration   string            `json:"configuration"`
	Enabled         bool              `json:"enabled"`
	Removed         bool              `json:"removed"`
}

// CompanionRecorded is a parent-owned relationship fact, not a child readiness event.
var CompanionRecorded = session.DefineEvent[Companion]("companion/instance")

// Clone returns the relationship value, which contains no mutable configuration.
func (entry Companion) Clone() Companion {
	return entry
}

// Validate checks a complete record before persistence or resource acquisition.
func (entry Companion) Validate() error {
	if entry.ParentSessionID == "" || entry.ChildSessionID == "" ||
		strings.TrimSpace(string(entry.ParentSessionID)) != string(entry.ParentSessionID) ||
		strings.TrimSpace(string(entry.ChildSessionID)) != string(entry.ChildSessionID) ||
		entry.ParentSessionID == entry.ChildSessionID || ValidateName(entry.Name) != nil ||
		entry.Revision <= 0 || entry.Revision > maximumRevision ||
		entry.SourceRevision < 0 || entry.SourceRevision > maximumRevision ||
		ValidateName(entry.Configuration) != nil ||
		entry.Removed && entry.Enabled {
		return ErrInvalid
	}
	return nil
}

// CompanionsFromEvents reconstructs only the specified parent's facts. Tombstones remain in
// the returned name-sorted view so automatic startup cannot recreate removals.
func CompanionsFromEvents(parentSessionID session.SessionID, events []session.Event) ([]Companion, error) {
	if parentSessionID == "" {
		return nil, ErrInvalid
	}
	latest := make(map[string]Companion)
	children := make(map[session.SessionID]bool)
	for _, entry := range events {
		if entry.Type != "companion/instance" {
			continue
		}
		var next Companion
		decoder := json.NewDecoder(bytes.NewReader(entry.Data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&next); err != nil || !json.Valid(entry.Data) {
			return nil, fmt.Errorf("%w: malformed Companion event", ErrHistory)
		}
		if next.ParentSessionID == "" {
			return nil, ErrHistory
		}
		if next.ParentSessionID != parentSessionID {
			continue
		}
		if err := next.Validate(); err != nil {
			return nil, fmt.Errorf("%w: invalid Companion record", ErrHistory)
		}
		previous, exists := latest[next.Name]
		if err := validateTransition(previous, exists, next); err != nil {
			return nil, fmt.Errorf("%w: inconsistent Companion transition", ErrHistory)
		}
		if !exists || previous.ChildSessionID != next.ChildSessionID {
			if children[next.ChildSessionID] {
				return nil, fmt.Errorf("%w: child Session reused", ErrHistory)
			}
			children[next.ChildSessionID] = true
		}
		latest[next.Name] = next
	}
	result := make([]Companion, 0, len(latest))
	for _, entry := range latest {
		result = append(result, entry)
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Name < result[right].Name })
	return result, nil
}

func validateTransition(previous Companion, exists bool, next Companion) error {
	if !exists {
		if next.Revision != 1 || next.Removed {
			return ErrConflict
		}
		return nil
	}
	if previous.Revision >= maximumRevision || next.Revision != previous.Revision+1 {
		return ErrConflict
	}
	if previous.ChildSessionID != next.ChildSessionID {
		if !previous.Removed || next.Removed || previous.Configuration == next.Configuration {
			return ErrConflict
		}
		return nil
	}
	if previous.Removed && !next.Removed || previous.SourceRevision != next.SourceRevision ||
		previous.Configuration != next.Configuration {
		return ErrConflict
	}
	return nil
}
