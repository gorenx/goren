package llm

import "encoding/json"

// ToolSchema is the model-facing JSON Schema description of one callable
// tool. Tool registry and execution policy remain owned outside llm.
type ToolSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}
