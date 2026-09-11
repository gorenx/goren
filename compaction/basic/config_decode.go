package basic

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gorenx/goren/internal/jsonvalue"
)

// DecodeConfig validates one strict JSON object and resolves owner-defined policy.
// It performs no construction, registration, or framework lifecycle operations.
func DecodeConfig(encoded json.RawMessage) (ResolvedConfig, error) {
	if err := jsonvalue.Validate(encoded); err != nil {
		return ResolvedConfig{}, fmt.Errorf("compaction-basic: decode configuration: %w", err)
	}
	trimmed := bytes.TrimSpace(encoded)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return ResolvedConfig{}, errors.New("compaction-basic: configuration must be a JSON object")
	}
	var settings Config
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		return ResolvedConfig{}, fmt.Errorf("compaction-basic: decode configuration: %w", err)
	}
	return ResolveConfig(settings)
}
