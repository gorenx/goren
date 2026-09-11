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
// Initial is the frozen first-creation configuration, not the child's current
// adopted configuration. Runtime lifetimes and addresses never belong here.
type Companion struct {
	ParentSessionID session.SessionID `json:"parentSessionID"`
	Name            string            `json:"name"`
	ChildSessionID  session.SessionID `json:"childSessionID"`
	Revision        int64             `json:"revision"`
	SourceRevision  int64             `json:"sourceRevision"`
	Initial         Config            `json:"initial"`
	Enabled         bool              `json:"enabled"`
	Removed         bool              `json:"removed"`
}

// CompanionRecorded is a parent-owned relationship fact, not a child readiness event.
var CompanionRecorded = session.DefineEvent[Companion]("companion/instance")

// Clone detaches the creation configuration from the caller's mutable values.
func (entry Companion) Clone() Companion {
	entry.Initial = entry.Initial.Clone()
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
		entry.Removed && entry.Enabled {
		return ErrInvalid
	}
	selection := entry.Initial.Inherit
	if selection.Model || selection.Prompt || selection.MCP || selection.Tools == InheritAllTools {
		return fmt.Errorf("%w: initial configuration still requires inheritance", ErrInvalid)
	}
	return entry.Initial.Validate()
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
		if err := json.Unmarshal(entry.Data, &next); err != nil {
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
		if next.Revision != 1 || next.Removed || next.Enabled != next.Initial.Enabled {
			return ErrConflict
		}
		return nil
	}
	if previous.Revision >= maximumRevision || next.Revision != previous.Revision+1 {
		return ErrConflict
	}
	if previous.ChildSessionID != next.ChildSessionID {
		if !previous.Removed || next.Removed || next.Enabled != next.Initial.Enabled {
			return ErrConflict
		}
		return nil
	}
	if previous.Removed && !next.Removed || previous.SourceRevision != next.SourceRevision ||
		!sameConfig(previous.Initial, next.Initial) {
		return ErrConflict
	}
	return nil
}

func sameConfig(left, right Config) bool {
	first, err := json.Marshal(left)
	if err != nil {
		return false
	}
	second, err := json.Marshal(right)
	return err == nil && bytes.Equal(first, second)
}
