package compaction

import (
	"encoding/json"
	"errors"

	"github.com/gorenx/goren/llm"
)

const checkpointPluginName = "compact"

// CheckpointSource is the correlated provenance of a replacement user message
// produced by any Compaction Provider.
type CheckpointSource struct {
	Kind            string  `json:"kind"`
	Plugin          string  `json:"plugin"`
	CompactionID    ID      `json:"compactionId"`
	SourceCommandID *string `json:"sourceCommandId,omitempty"`
}

// NewCheckpointSource constructs exact source-compatible provenance.
func NewCheckpointSource(
	compactionID ID,
	sourceCommandID *string,
) (CheckpointSource, error) {
	if compactionID == "" {
		return CheckpointSource{}, errors.New(
			"compaction: checkpoint source needs a compactionId",
		)
	}
	if sourceCommandID != nil && *sourceCommandID == "" {
		return CheckpointSource{}, errors.New(
			"compaction: checkpoint sourceCommandId is empty",
		)
	}
	return CheckpointSource{
		Kind:            "plugin",
		Plugin:          checkpointPluginName,
		CompactionID:    compactionID,
		SourceCommandID: cloneString(sourceCommandID),
	}, nil
}

// SourceKind implements llm.MessageSource.
func (CheckpointSource) SourceKind() string {
	return "plugin"
}

// CloneSource validates and detaches checkpoint provenance.
func (origin CheckpointSource) CloneSource() (llm.MessageSource, error) {
	return NewCheckpointSource(
		origin.CompactionID,
		origin.SourceCommandID,
	)
}

// MarshalJSON fixes the merge-extension discriminants on every write.
func (origin CheckpointSource) MarshalJSON() ([]byte, error) {
	validated, err := NewCheckpointSource(
		origin.CompactionID,
		origin.SourceCommandID,
	)
	if err != nil {
		return nil, err
	}
	type wireSource CheckpointSource
	return json.Marshal(wireSource(validated))
}

// IsCheckpointSource recognizes typed and losslessly restored sources without
// making llm depend on this package.
func IsCheckpointSource(origin llm.MessageSource) bool {
	if origin == nil || origin.SourceKind() != "plugin" {
		return false
	}
	if _, typed := origin.(CheckpointSource); typed {
		return true
	}
	encoded, err := json.Marshal(origin)
	if err != nil {
		return false
	}
	var marker struct {
		Kind   string `json:"kind"`
		Plugin string `json:"plugin"`
	}
	return json.Unmarshal(encoded, &marker) == nil &&
		marker.Kind == "plugin" &&
		marker.Plugin == checkpointPluginName
}

func decodeCheckpointSource(
	origin llm.MessageSource,
) (CheckpointSource, bool, error) {
	if !IsCheckpointSource(origin) {
		return CheckpointSource{}, false, nil
	}
	encoded, err := json.Marshal(origin)
	if err != nil {
		return CheckpointSource{}, true, err
	}
	var wireValue CheckpointSource
	if err := decodeStrictCompactionJSON(encoded, &wireValue); err != nil {
		return CheckpointSource{}, true, err
	}
	if wireValue.Kind != "plugin" || wireValue.Plugin != checkpointPluginName {
		return CheckpointSource{}, true, errors.New(
			"compaction: checkpoint source marker does not match",
		)
	}
	validated, err := NewCheckpointSource(
		wireValue.CompactionID,
		wireValue.SourceCommandID,
	)
	return validated, true, err
}

func cloneString(source *string) *string {
	if source == nil {
		return nil
	}
	detached := *source
	return &detached
}
