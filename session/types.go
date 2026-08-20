// Package session owns the append-only Session log, its lifecycle, and the
// ordered model-visible surface derived from committed events.
package session

import (
	"errors"
	"fmt"
	"path/filepath"
)

// FormatVersion is stamped into every Session Header persisted by an adapter.
// Version 0 matches the pinned DeepSeek Harness session format.
const FormatVersion = 0

const maxSafeInteger int64 = 1<<53 - 1

// SessionID identifies one Session in a LiveStore and in persistence artifacts.
type SessionID string

// Origin classifies how a Session was created for presentation purposes.
type Origin string

const (
	// OriginSubagent identifies a child created by agent delegation.
	OriginSubagent Origin = "subagent"
)

// Header is immutable Session metadata kept outside the conversation event log.
type Header struct {
	Version         int        `json:"version"`
	ID              SessionID  `json:"id"`
	CreatedAt       int64      `json:"createdAt"`
	CWD             *string    `json:"cwd,omitempty"`
	ParentSession   *SessionID `json:"parentSession,omitempty"`
	SeedLength      *int64     `json:"seedLength,omitempty"`
	Origin          Origin     `json:"origin,omitempty"`
	DelegationDepth *int64     `json:"delegationDepth,omitempty"`
	AgentPreset     *string    `json:"agentPreset,omitempty"`
}

// Metadata contains caller-supplied Header fields for a newly created Session.
type Metadata struct {
	CreatedAt       *int64
	CWD             *string
	ParentSession   *SessionID
	SeedLength      *int64
	Origin          Origin
	DelegationDepth *int64
	AgentPreset     *string
}

// CreateOptions supplies an optional replay seed and creation metadata.
type CreateOptions struct {
	Seed     []Event
	Metadata Metadata
}

func buildHeader(identifier SessionID, sessionMetadata Metadata, temporalSource TimeSource) (Header, error) {
	createdAt := temporalSource.CurrentTime().UnixMilli()
	if sessionMetadata.CreatedAt != nil {
		createdAt = *sessionMetadata.CreatedAt
	}
	candidate := Header{
		Version: FormatVersion, ID: identifier, CreatedAt: createdAt,
		CWD: cloneString(sessionMetadata.CWD), ParentSession: cloneSessionID(sessionMetadata.ParentSession),
		SeedLength: cloneInt64(sessionMetadata.SeedLength), Origin: sessionMetadata.Origin,
		DelegationDepth: cloneInt64(sessionMetadata.DelegationDepth), AgentPreset: cloneString(sessionMetadata.AgentPreset),
	}
	if err := validateHeader(identifier, candidate); err != nil {
		return Header{}, err
	}
	return candidate, nil
}

func validateHeader(identifier SessionID, candidate Header) error {
	if candidate.Version != FormatVersion {
		return fmt.Errorf("session: header version must be %d, got %d", FormatVersion, candidate.Version)
	}
	if candidate.ID != identifier {
		return fmt.Errorf("session: header id %q does not match session id %q", candidate.ID, identifier)
	}
	if !isSafeNonNegative(candidate.CreatedAt) {
		return errors.New("session: header createdAt must be a non-negative safe integer")
	}
	if candidate.CWD != nil && !filepath.IsAbs(*candidate.CWD) {
		return fmt.Errorf("session: header cwd must be an absolute path, got %q", *candidate.CWD)
	}
	if candidate.SeedLength != nil && !isSafeNonNegative(*candidate.SeedLength) {
		return errors.New("session: header seedLength must be a non-negative safe integer")
	}
	if candidate.Origin != "" && candidate.Origin != OriginSubagent {
		return errors.New("session: header origin must be \"subagent\"")
	}
	if candidate.DelegationDepth != nil && !isSafeNonNegative(*candidate.DelegationDepth) {
		return errors.New("session: header delegationDepth must be a non-negative safe integer")
	}
	return nil
}

func isSafeNonNegative(value int64) bool {
	return value >= 0 && value <= maxSafeInteger
}

func cloneHeader(source Header) Header {
	snapshot := source
	snapshot.CWD = cloneString(source.CWD)
	snapshot.ParentSession = cloneSessionID(source.ParentSession)
	snapshot.SeedLength = cloneInt64(source.SeedLength)
	snapshot.DelegationDepth = cloneInt64(source.DelegationDepth)
	snapshot.AgentPreset = cloneString(source.AgentPreset)
	return snapshot
}

func cloneSessionID(source *SessionID) *SessionID {
	if source == nil {
		return nil
	}
	snapshot := *source
	return &snapshot
}

func cloneInt64(source *int64) *int64 {
	if source == nil {
		return nil
	}
	snapshot := *source
	return &snapshot
}

func cloneString(source *string) *string {
	if source == nil {
		return nil
	}
	snapshot := *source
	return &snapshot
}
