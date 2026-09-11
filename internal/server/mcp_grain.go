package server

import (
	"context"
	"errors"

	mcpapi "github.com/gorenx/goren/apiproxy/mcp"
	"github.com/gorenx/goren/mcpclient"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

// mcpGrain serializes inbound management only. Tool calls bypass this mailbox.
type mcpGrain struct {
	manager *mcpManager
	// pending maps framework replies to cancellation handles. Business connection
	// state and write serialization remain owned by mcpManager.
	pending map[*goakt.GrainReply]context.CancelFunc
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

type mcpConfiguration struct {
	ctx         context.Context
	definitions []mcpclient.Config
}

type mcpValidation struct {
	ctx         context.Context
	definitions []mcpclient.Config
}

type mcpResponse struct {
	reply  *goakt.GrainReply
	result mcpReply
}

func (*mcpGrain) OnActivate(context.Context, *goakt.GrainProps) error { return nil }
func (owner *mcpGrain) OnDeactivate(context.Context, *goakt.GrainProps) error {
	for reply, cancel := range owner.pending {
		cancel()
		reply.Response(mcpReply{
			err: errors.New("server: MCP operation completion is unconfirmed after deactivation"),
		})
		delete(owner.pending, reply)
	}
	return nil
}

func (owner *mcpGrain) OnReceive(gc *goakt.GrainContext) {
	switch input := gc.Message().(type) {
	case bindMCP:
		if owner.manager != nil || input.manager == nil {
			gc.Response(mcpReply{
				err: errors.New("server: MCP manager requires one binding"),
			})
			return
		}
		if err := gc.EnableReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))); err != nil {
			gc.Response(mcpReply{
				err: err,
			})
			return
		}
		owner.manager = input.manager
		owner.pending = make(map[*goakt.GrainReply]context.CancelFunc)
		gc.Response(mcpReply{})
	case mcpConfiguration:
		manager := owner.manager
		owner.execute(gc, input.ctx, func(ctx context.Context) mcpReply {
			return mcpReply{
				err: manager.Reconcile(ctx, input.definitions),
			}
		})
	case mcpValidation:
		if owner.manager == nil {
			gc.Response(mcpReply{
				err: errors.New("server: MCP manager unavailable"),
			})
			return
		}
		gc.Response(mcpReply{
			err: owner.manager.Validate(input.ctx, input.definitions),
		})
	case mcpResponse:
		gc.NoErr()
		if cancel, exists := owner.pending[input.reply]; exists {
			cancel()
			delete(owner.pending, input.reply)
			input.reply.Response(input.result)
		}
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
		if input.operation == "list" {
			gc.Response(mcpReply{
				views: owner.manager.listConnections(),
			})
			return
		}
		manager := owner.manager
		owner.execute(gc, input.ctx, func(ctx context.Context) mcpReply {
			var result mcpReply
			switch input.operation {
			case "connect":
				view, err := manager.connect(ctx, mcpclient.Config{
					ServerName: input.input.ServerName,
					Transport:  "streamable-http",
					URL:        input.input.URL,
				})
				result.views = []mcpapi.View{view}
				result.err = err
			case "disconnect":
				result.err = manager.disconnect(ctx, input.input.ServerName)
			default:
				result.err = errors.New("server: unknown MCP operation")
			}
			return result
		})
	default:
		gc.Unhandled()
	}
}

func (owner *mcpGrain) execute(gc *goakt.GrainContext, ctx context.Context, run func(context.Context) mcpReply) {
	if owner.manager == nil || ctx == nil {
		gc.Response(mcpReply{
			err: errors.New("server: MCP manager unavailable"),
		})
		return
	}
	if err := ctx.Err(); err != nil {
		gc.Response(mcpReply{
			err: err,
		})
		return
	}
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(mcpReply{
			err: errors.New("server: asynchronous MCP request is required"),
		})
		return
	}
	work, cancel := context.WithCancel(ctx)
	owner.pending[reply] = cancel
	err := gc.PipeToSelf(func() (any, error) {
		return mcpResponse{
			reply:  reply,
			result: run(work),
		}, nil
	})
	if err != nil {
		cancel()
		delete(owner.pending, reply)
		reply.Response(mcpReply{
			err: err,
		})
	}
}
