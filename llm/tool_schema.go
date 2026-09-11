package llm

import "encoding/json"

// ToolSchema is the model-facing JSON Schema description of one callable
// tool. Tool registry and execution policy remain owned outside llm.
type ToolSchema struct {
	// Version identifies this tool definition within its Session. Registry assigns
	// it independently of other tools; it is never sent to the model provider.
	Version     uint64          `json:"-"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}
