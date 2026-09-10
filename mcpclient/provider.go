package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/gorenx/goren/tools"
	"github.com/gorenx/goren/tools/tool"
)

type provider struct {
	connection *Connection
	name       string
}

func (source *provider) Open(ctx context.Context) (tool.Implementation, error) {
	if err := ctx.Err(); err != nil {
		return tool.Implementation{}, err
	}
	source.connection.mutex.Lock()
	entry, exists := source.connection.entries[source.name]
	closed := source.connection.closed
	source.connection.mutex.Unlock()
	if !exists || closed {
		return tool.Implementation{}, errors.New("mcpclient: tool is no longer available")
	}
	body := &executor{
		connection:   source.connection,
		rawName:      entry.rawName,
		taskRequired: entry.taskRequired,
	}
	return tool.Implementation{
		Definition: tools.ToolDefinition{
			Name:        entry.schema.Name,
			Description: entry.schema.Description,
			Parameters:  append(json.RawMessage(nil), entry.schema.Parameters...),
			Executor:    body,
			Timeout:     time.Duration(source.connection.settings.ElicitationTimeoutMs) * time.Millisecond,
			Output: tools.ToolOutputDefinition{
				Schema:   json.RawMessage(`{"type":"object","properties":{"content":{"type":"array","items":{}},"structuredContent":{}},"required":["content"],"additionalProperties":false}`),
				Renderer: &renderer{},
			},
		},
	}, nil
}
