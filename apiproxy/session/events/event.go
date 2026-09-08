package sessionevents

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// LogEvent preserves the strict session-log envelope while leaving data
// and surfaceOp with their owner-defined JSON shapes.
type LogEvent struct {
	Type            string          `json:"type"`
	Seq             int64           `json:"seq"`
	Time            int64           `json:"time"`
	Data            json.RawMessage `json:"data"`
	SourceEventSeqs *[]int64        `json:"sourceEventSeqs,omitempty"`
	SurfaceOp       json.RawMessage `json:"surfaceOp,omitempty"`
	Ignorable       bool            `json:"ignorable,omitempty"`
}

// ToolView is the call/result render-intent wrapper. View stays wide
// because the registered Tool presenter owns its card-specific fields.
type ToolView struct {
	For  string          `json:"for"`
	View json.RawMessage `json:"view"`
}

func validateLogEvent(record LogEvent) error {
	if record.Seq < 0 {
		return errors.New("session event seq must be non-negative")
	}
	if record.Data != nil && !json.Valid(record.Data) {
		return errors.New("session event data must be JSON")
	}
	if record.SurfaceOp != nil && !json.Valid(record.SurfaceOp) {
		return errors.New("session event surfaceOp must be JSON")
	}
	return nil
}

func validateToolView(presentation *ToolView) error {
	if presentation == nil {
		return nil
	}
	if presentation.For != "call" && presentation.For != "result" {
		return errors.New("tool event view for must be call or result")
	}
	return validateJSONObjectWithString("tool event view", presentation.View, "card")
}

func validateJSONObjectWithString(label string, raw json.RawMessage, field string) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) < 2 || trimmed[0] != '{' || !json.Valid(trimmed) {
		return fmt.Errorf("%s must be a JSON object", label)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &fields); err != nil || fields == nil {
		return fmt.Errorf("%s must be a JSON object", label)
	}
	var value string
	if err := json.Unmarshal(fields[field], &value); err != nil {
		return fmt.Errorf("%s %s must be a string", label, field)
	}
	return nil
}
