package projectioncache

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gorenx/goren/internal/jsonvalue"
	"github.com/gorenx/goren/session"
	sessionprojection "github.com/gorenx/goren/session/projection"
)

// LogIdentity prevents a reused Session ID from inheriting another Session
// lifecycle's rebuildable state.
type LogIdentity struct {
	CreatedAt int64   `json:"createdAt"`
	CWD       *string `json:"cwd,omitempty"`
}

// CheckpointRecord is one Session's current complete set of Unit rows.
type CheckpointRecord struct {
	Identity LogIdentity                  `json:"identity"`
	Rows     sessionprojection.Checkpoint `json:"rows"`
}

func identityOf(metadata session.Header) LogIdentity {
	return LogIdentity{
		CreatedAt: metadata.CreatedAt,
		CWD:       cloneString(metadata.CWD),
	}
}

func sameIdentity(left LogIdentity, right LogIdentity) bool {
	return left.CreatedAt == right.CreatedAt && sameString(left.CWD, right.CWD)
}

func identityMatchesHeader(identity LogIdentity, metadata session.Header) bool {
	return identity.CreatedAt == metadata.CreatedAt && sameString(identity.CWD, metadata.CWD)
}

func sameString(left *string, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

// ValidateCheckpointRecord validates and detaches one owner-defined persisted
// record before it crosses a storage or in-memory index boundary.
func ValidateCheckpointRecord(
	identifier session.SessionID,
	record CheckpointRecord,
) (CheckpointRecord, error) {
	if identifier == "" {
		return CheckpointRecord{}, errors.New("checkpoint Session ID is empty")
	}
	if record.Identity.CreatedAt < 0 {
		return CheckpointRecord{}, errors.New("checkpoint createdAt is negative")
	}
	for projectionKey, row := range record.Rows {
		if projectionKey == "" {
			return CheckpointRecord{}, errors.New("checkpoint projection key is empty")
		}
		if row.Version < 0 {
			return CheckpointRecord{}, fmt.Errorf("checkpoint %q version is negative", projectionKey)
		}
		if row.Seq < -1 {
			return CheckpointRecord{}, fmt.Errorf("checkpoint %q seq is below -1", projectionKey)
		}
		if err := jsonvalue.Validate(row.Value); err != nil {
			return CheckpointRecord{}, fmt.Errorf(
				"checkpoint %q value is not valid plain JSON: %w",
				projectionKey,
				err,
			)
		}
	}
	return cloneRecord(record), nil
}

func cloneRecord(record CheckpointRecord) CheckpointRecord {
	detached := CheckpointRecord{
		Identity: LogIdentity{
			CreatedAt: record.Identity.CreatedAt,
			CWD:       cloneString(record.Identity.CWD),
		},
		Rows: make(sessionprojection.Checkpoint, len(record.Rows)),
	}
	for projectionKey, row := range record.Rows {
		row.Value = append(json.RawMessage(nil), row.Value...)
		detached.Rows[projectionKey] = row
	}
	return detached
}

func cloneString(source *string) *string {
	if source == nil {
		return nil
	}
	detached := *source
	return &detached
}

func checkpointDominates(
	current sessionprojection.Checkpoint,
	candidate sessionprojection.Checkpoint,
) bool {
	for projectionKey, candidateRow := range candidate {
		currentRow, found := current[projectionKey]
		if !found || currentRow.Version != candidateRow.Version ||
			currentRow.Seq < candidateRow.Seq {
			return false
		}
	}
	return true
}
