package mcpclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

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

// ProviderID identifies this connection's immutable Provider binding.
// Refresh and Reconnect retain it; a different configured Connection changes it.
func (owner *Connection) ProviderID() tools.ProviderID { return owner.providerID }

// Provider resolves one explicit public/raw binding without network I/O.
func (owner *Connection) Provider(name string) (tool.Provider, bool) {
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	if owner.closed {
		return nil, false
	}
	_, present := owner.current.tool(name)
	if !present {
		return nil, false
	}
	return &provider{
		connection: owner,
		name:       name,
	}, true
}

// openTool captures an executable definition under this Connection's admission policy.
func (owner *Connection) openTool(ctx context.Context, name string) (tool.Implementation, error) {
	if ctx == nil {
		return tool.Implementation{}, errors.New("mcpclient: tool context is required")
	}
	if err := ctx.Err(); err != nil {
		return tool.Implementation{}, err
	}
	owner.mutex.Lock()
	peer := owner.current
	entry, exists := peer.tool(name)
	closed := owner.closed
	paused := owner.paused
	owner.mutex.Unlock()
	if paused {
		return tool.Implementation{}, ErrDraining
	}
	if !exists || closed {
		return tool.Implementation{}, errors.New("mcpclient: tool is no longer available")
	}
	body := &executor{
		connection: owner,
		definition: entry,
	}
	return tool.Implementation{
		Definition: tools.ToolDefinition{
			Name:        entry.schema.Name,
			Description: entry.schema.Description,
			Parameters:  append(json.RawMessage(nil), entry.schema.Parameters...),
			Executor:    body,
			Timeout:     time.Duration(owner.settings.ElicitationTimeoutMs) * time.Millisecond,
			Output: tools.ToolOutputDefinition{
				Schema:   json.RawMessage(`{"type":"object","properties":{"content":{"type":"array","items":{}},"structuredContent":{}},"required":["content"],"additionalProperties":false}`),
				Renderer: &renderer{},
			},
		},
	}, nil
}
