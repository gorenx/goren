package apiproxy

import (
	"context"
	"encoding/json"

	"github.com/gorenx/goren/connection"
)

// ServiceName preserves the canonical Host capability name.
const ServiceName = "apiProxy"

// Service is the complete transport-neutral Host surface consumed by the
// Connection adapter.
type Service interface {
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
