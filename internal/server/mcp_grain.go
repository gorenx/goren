package server

import (
	"context"
	"errors"

	mcpapi "github.com/gorenx/goren/apiproxy/mcp"
	"github.com/gorenx/goren/mcpclient"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// mcpGrain serializes inbound management only. Tool calls bypass this mailbox.
type mcpGrain struct {
	manager *mcpManager
}

type bindMCP struct {
	manager *mcpManager
}

// mcpCommand is process-local; request cancellation must reach blocking SDK I/O.
type mcpCommand struct {
	ctx       context.Context
	operation string
	input     mcpapi.ConnectRequest
}

type mcpReply struct {
	views []mcpapi.View
	err   error
}

func (*mcpGrain) OnActivate(context.Context, *goakt.GrainProps) error   { return nil }
func (*mcpGrain) OnDeactivate(context.Context, *goakt.GrainProps) error { return nil }

func (owner *mcpGrain) OnReceive(gc *goakt.GrainContext) {
	switch input := gc.Message().(type) {
	case bindMCP:
		if owner.manager != nil || input.manager == nil {
			gc.Response(mcpReply{
				err: errors.New("server: MCP manager requires one binding"),
			})
			return
		}
		owner.manager = input.manager
		gc.Response(mcpReply{})
	case mcpCommand:
		if owner.manager == nil || input.ctx == nil {
			gc.Response(mcpReply{
				err: errors.New("server: MCP manager unavailable"),
			})
			return
		}
		if err := input.ctx.Err(); err != nil {
			gc.Response(mcpReply{
				err: err,
			})
			return
		}
		var result mcpReply
		switch input.operation {
		case "connect":
			view, err := owner.manager.connect(input.ctx, mcpclient.Config{
				ServerName: input.input.ServerName,
				Transport:  "streamable-http",
				URL:        input.input.URL,
			})
			result.views = []mcpapi.View{view}
			result.err = err
		case "list":
			result.views = owner.manager.listConnections()
		case "disconnect":
			result.err = owner.manager.disconnect(input.ctx, input.input.ServerName)
		default:
			result.err = errors.New("server: unknown MCP operation")
		}
		gc.Response(result)
	default:
		gc.Unhandled()
	}
}
