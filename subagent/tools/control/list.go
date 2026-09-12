package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/tools"
)

var listParameters = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "scope": {"type": "string", "enum": ["children", "descendants"]}
  }
}`)

var listOutput = json.RawMessage(`{
  "type": "array",
  "items": {
    "oneOf": [
      {
        "type": "object",
        "additionalProperties": false,
        "properties": {
          "kind": {"type": "string", "const": "child"},
          "id": {"type": "string"},
          "label": {"type": "string"},
          "status": {"type": "string", "enum": ["running", "idle", "ready", "starting", "draining", "stopping", "stopped", "start_failed", "close_failed"]},
          "parent": {"type": "string"},
          "depth": {"type": "integer"}
        },
        "required": ["kind", "id", "label", "status"]
      },
      {
        "type": "object",
        "additionalProperties": false,
        "properties": {
          "kind": {"type": "string", "const": "diagnostic"},
          "id": {"type": "string"},
          "reason": {"type": "string", "enum": ["corrupt", "unsupported", "unavailable"]},
          "parent": {"type": "string"},
          "depth": {"type": "integer"}
        },
        "required": ["kind", "id", "reason"]
      }
    ]
  }
}`)

type listArguments struct {
	Scope string `json:"scope,omitempty"`
}

const listDescription = "List background agents by durable id, label and observed state without starting or resuming them. children selects direct children; descendants follows the configured directory tree."

func (owner *controlTools) listDefinition() tools.ToolDefinition {
	adapter := &listTool{
		source: &directoryLister{
			directory: owner.directory,
			activity:  owner.activity,
		},
	}
	return adapter.definition(llm.ToolSchema{
		Name:        "list_agents",
		Description: listDescription,
		Parameters:  listParameters,
	})
}

func (adapter *listTool) definition(input llm.ToolSchema) tools.ToolDefinition {
	return tools.ToolDefinition{
		Name:        input.Name,
		Description: input.Description,
		Parameters:  append([]byte(nil), input.Parameters...),
		Output: tools.ToolOutputDefinition{
			Schema:   append([]byte(nil), listOutput...),
			Renderer: adapter,
		},
		Executor: adapter,
	}
}

func (adapter *listTool) Execute(
	rawArguments json.RawMessage,
	runContext tools.ToolRunContext,
) (json.RawMessage, error) {
	if runContext.Context == nil {
		return nil, errors.New("list_agents requires a context")
	}
	if err := runContext.Context.Err(); err != nil {
		return nil, err
	}
	parentID := runContext.Execution.SessionID
	if parentID == "" {
		return nil, errors.New("list_agents requires a calling Agent")
	}
	var request listArguments
	if decodeErr := json.Unmarshal(rawArguments, &request); decodeErr != nil {
		return nil, decodeErr
	}
	if request.Scope == "" {
		request.Scope = "children"
	}
	if request.Scope != "children" && request.Scope != "descendants" {
		return nil, fmt.Errorf("list_agents: unsupported scope %q", request.Scope)
	}
	entries, err := adapter.source.List(runContext.Context, Listing{
		ParentSessionID: parentID,
		Scope:           request.Scope,
	})
	if err != nil {
		return nil, err
	}
	if entries == nil {
		entries = []Entry{}
	}

	return json.Marshal(entries)
}

func (adapter *listTool) Render(
	rawArguments json.RawMessage,
	rawValue json.RawMessage,
) ([]agentmessage.ContentBlock, error) {
	var request listArguments
	if decodeErr := json.Unmarshal(rawArguments, &request); decodeErr != nil {
		return nil, decodeErr
	}
	if request.Scope == "" {
		request.Scope = "children"
	}
	var entries []Entry
	if decodeErr := json.Unmarshal(rawValue, &entries); decodeErr != nil {
		return nil, decodeErr
	}
	if len(entries) == 0 {
		return []agentmessage.ContentBlock{
			agentmessage.NewTextBlock("(no subagents)"),
		}, nil
	}
	lines := make([]string, 0, len(entries))
	for _, entry := range entries {
		position := ""
		if request.Scope == "descendants" {
			position = fmt.Sprintf(
				" parent=%s depth=%d",
				valueOrEmpty(entry.Parent),
				int64OrZero(entry.Depth),
			)
		}
		if entry.Kind == "child" {
			lines = append(
				lines,
				fmt.Sprintf(
					"%s [%s]%s — %s",
					entry.ID,
					entry.Status,
					position,
					entry.Label,
				),
			)
		} else {
			lines = append(
				lines,
				fmt.Sprintf(
					"%s [diagnostic: %s]%s",
					entry.ID,
					entry.Reason,
					position,
				),
			)
		}
	}
	return []agentmessage.ContentBlock{
		agentmessage.NewTextBlock(strings.Join(lines, "\n")),
	}, nil
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func int64OrZero(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

type listTool struct {
	// source owns directory interpretation and lifecycle observation.
	source Lister
}
