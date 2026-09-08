package interaction

import (
	"github.com/gorenx/goren/apiproxy/stream"
	"github.com/gorenx/goren/connection"
)

// FrameBroker owns the mux lifecycle of answerable interaction
// frames. A requested frame remains replayable with the same rpcId until its
// owner resolves or withdraws it.
type FrameBroker interface {
	PublishPending(connection.RPCID, stream.MuxFrame) error
	ResolvePending(connection.RPCID, stream.MuxFrame) error
}
