package apiproxy

import (
	"context"
	"encoding/json"

	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/plugin"
)

const (
	// PluginName is the canonical Harness API Proxy Plugin name.
	PluginName = "@deepseek-ai/dsh-host-apiproxy"
	// ServiceName preserves the canonical Cordis capability name for
	// diagnostics and source traceability.
	ServiceName = "apiProxy"
)

// Service is the complete transport-neutral Host surface consumed by the
// Connection adapter.
type Service interface {
	plugin.Service
	HasUnary(string) bool
	DispatchUnary(
		context.Context,
		string,
		connection.RPCID,
		json.RawMessage,
	) (connection.RPCResult, error)
	Respond(
		context.Context,
		connection.ClientResponse,
	) (connection.RPCReceipt, error)
	Mux(context.Context, func(connection.RPCRequest) error) error
	Host(context.Context, func(connection.RPCRequest) error) error
}
