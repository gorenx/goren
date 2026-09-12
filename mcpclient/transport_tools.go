package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/tools"
)

// Refresh validates every page before replacing the last complete tool snapshot.
func (owner *transport) Refresh(ctx context.Context) error {
	owner.refresh.Lock()
	defer owner.refresh.Unlock()
	work, finish, err := owner.begin(ctx)
	if err != nil {
		return err
	}
	defer finish()
	entries := make(map[string]remoteTool)
	cursors := make(map[string]bool)
	cursor := ""
	for page := 0; ; page++ {
		if page >= owner.settings.MaxPages || cursors[cursor] {
			return errors.New("mcpclient: tools/list pagination limit or repeated cursor")
		}
		cursors[cursor] = true
		listed, err := owner.session.listToolsPage(work, cursor)
		if err != nil {
			return errors.New("mcpclient: tools/list failed")
		}
		if listed == nil || listed.Tools == nil {
			return errors.New("mcpclient: empty tools/list response")
		}
		for _, advertised := range listed.Tools {
			if advertised.Name == "" {
				return errors.New("mcpclient: empty remote tool name")
			}
			name := ToolName(owner.settings.ServerName, advertised.Name)
			if _, exists := entries[name]; exists {
				return errors.New("mcpclient: duplicate tool name")
			}
			if len(entries) >= owner.settings.MaxTools {
				return errors.New("mcpclient: tool count exceeds limit")
			}
			parameters := advertised.RawInputSchema
			if parameters == nil {
				parameters, err = json.Marshal(advertised.InputSchema)
			}
			if err != nil {
				return errors.New("mcpclient: invalid tool input schema")
			}
			parameters, err = tools.SnapshotObjectSchema(parameters)
			if err != nil {
				return errors.New("mcpclient: unsupported tool input schema")
			}
			entries[name] = remoteTool{
				rawName: advertised.Name,
				schema: llm.ToolSchema{
					Name:        name,
					Description: advertised.Description,
					Parameters:  parameters,
				},
				taskRequired: advertised.Execution != nil && advertised.Execution.TaskSupport == "required",
			}
		}
		cursor = string(listed.NextCursor)
		if cursor == "" {
			break
		}
	}
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	if owner.closed || work.Err() != nil {
		return errors.New("mcpclient: connection ended during discovery")
	}
	owner.entries = entries
	return nil
}

// tool returns a detached entry from the last complete discovery snapshot.
func (owner *transport) tool(name string) (remoteTool, bool) {
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	entry, exists := owner.entries[name]
	entry.schema.Parameters = append(json.RawMessage(nil), entry.schema.Parameters...)
	return entry, exists
}
