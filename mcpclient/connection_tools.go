package mcpclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/tools"
	"github.com/gorenx/goren/tools/tool"
)

type remoteTool struct {
	rawName      string
	schema       llm.ToolSchema
	taskRequired bool
}

// ToolName qualifies a remote name for Goren's model tool namespace. Lossy names
// carry a digest; wire calls always use the separately retained original name.
func ToolName(serverName string, rawName string) string {
	original := "mcp_" + serverName + "_" + rawName
	clean := strings.Map(func(symbol rune) rune {
		if (symbol >= 'a' && symbol <= 'z') ||
			(symbol >= 'A' && symbol <= 'Z') ||
			(symbol >= '0' && symbol <= '9') ||
			symbol == '_' ||
			symbol == '-' {
			return symbol
		}
		return '_'
	}, original)
	if clean == original && len(clean) <= 64 {
		return clean
	}
	digest := sha256.Sum256([]byte(serverName + "\x00" + rawName))
	if len(clean) > 47 {
		clean = clean[:47]
	}
	return clean + "_" + hex.EncodeToString(digest[:8])
}

// Refresh validates every page before replacing the last complete tool snapshot.
func (owner *Connection) Refresh(ctx context.Context) error {
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

func schemas(entries map[string]remoteTool) []llm.ToolSchema {
	result := make([]llm.ToolSchema, 0, len(entries))
	for _, entry := range entries {
		detached := entry.schema
		detached.Parameters = append(json.RawMessage(nil), detached.Parameters...)
		result = append(result, detached)
	}
	sort.Slice(result, func(left int, right int) bool { return result[left].Name < result[right].Name })
	return result
}

// Provider resolves one explicit public/raw binding without network I/O.
func (owner *Connection) Provider(name string) (tool.Provider, bool) {
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	_, present := owner.entries[name]
	if !present || owner.closed {
		return nil, false
	}
	return &provider{
		connection: owner,
		name:       name,
	}, true
}
