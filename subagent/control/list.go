package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
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
          "status": {"type": "string", "enum": ["running", "idle", "ready"]},
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

type listEntry struct {
	Kind   string  `json:"kind"`
	ID     string  `json:"id"`
	Label  string  `json:"label,omitempty"`
	Status string  `json:"status,omitempty"`
	Reason string  `json:"reason,omitempty"`
	Parent *string `json:"parent,omitempty"`
	Depth  *int64  `json:"depth,omitempty"`
}

func (owner *Plugin) listDefinition() tools.ToolDefinition {
	return tools.ToolDefinition{
		Name:        "list_agents",
		Description: "List continuable background subagents by durable id and label. children lists direct children; descendants walks the complete tree without resuming cold Agents.",
		Parameters:  listParameters,
		Output: tools.ToolOutputDefinition{
			Schema:   listOutput,
			Renderer: tools.OutputRendererFunc(renderList),
		},
		Executor: tools.ExecutorFunc(owner.listAgents),
	}
}

func (owner *Plugin) listAgents(
	rawArguments json.RawMessage,
	runContext tools.ToolRunContext,
) (json.RawMessage, error) {
	parentAgent, matches := runContext.Execution.Subject.(agent.Agent)
	if !matches || parentAgent == nil {
		return nil, errors.New("list_agents requires a calling Agent")
	}
	var request listArguments
	if decodeErr := json.Unmarshal(rawArguments, &request); decodeErr != nil {
		return nil, decodeErr
	}
	if request.Scope == "" {
		request.Scope = "children"
	}
	entries := make([]listEntry, 0)
	switch request.Scope {
	case "children":
		rows, listErr := owner.catalog.ListChildren(
			runContext.Context,
			parentAgent.ID(),
		)
		if listErr != nil {
			return nil, listErr
		}
		for _, row := range rows {
			if projected, included := owner.project(row, nil, nil); included {
				entries = append(entries, projected)
			}
		}
	case "descendants":
		rows, listErr := owner.catalog.ListDescendants(
			runContext.Context,
			parentAgent.ID(),
		)
		if listErr != nil {
			return nil, listErr
		}
		for _, row := range rows {
			parentID := string(row.ParentID)
			depth := row.Depth
			if projected, included := owner.project(
				row.Entry,
				&parentID,
				&depth,
			); included {
				entries = append(entries, projected)
			}
		}
	default:
		return nil, fmt.Errorf("list_agents: unsupported scope %q", request.Scope)
	}
	return json.Marshal(entries)
}

func (owner *Plugin) project(
	entry subagent.ListEntry,
	parentID *string,
	depth *int64,
) (listEntry, bool) {
	switch row := entry.(type) {
	case subagent.ContinuableChildEntry:
		return listEntry{
			Kind:   "child",
			ID:     string(row.ID),
			Label:  row.Label,
			Status: owner.status(row.ID),
			Parent: parentID,
			Depth:  depth,
		}, true
	case subagent.DiagnosticEntry:
		return listEntry{
			Kind:   "diagnostic",
			ID:     string(row.ID),
			Reason: string(row.Reason),
			Parent: parentID,
			Depth:  depth,
		}, true
	case subagent.OneShotChildEntry:
		return listEntry{}, false
	default:
		return listEntry{}, false
	}
}

func (owner *Plugin) status(childID session.SessionID) string {
	childAgent, found := owner.agents.Get(childID)
	if !found {
		return "ready"
	}
	if childAgent.StatusValue() == agent.StatusRunning {
		return "running"
	}
	return "idle"
}

func renderList(
	rawArguments json.RawMessage,
	rawValue json.RawMessage,
) ([]llm.ContentBlock, error) {
	var request listArguments
	if decodeErr := json.Unmarshal(rawArguments, &request); decodeErr != nil {
		return nil, decodeErr
	}
	if request.Scope == "" {
		request.Scope = "children"
	}
	var entries []listEntry
	if decodeErr := json.Unmarshal(rawValue, &entries); decodeErr != nil {
		return nil, decodeErr
	}
	if len(entries) == 0 {
		return []llm.ContentBlock{
			llm.NewTextBlock("(no subagents)"),
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
	return []llm.ContentBlock{
		llm.NewTextBlock(strings.Join(lines, "\n")),
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
