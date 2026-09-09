package sessionapi

import (
	"context"

	sessionframes "github.com/gorenx/goren/apiproxy/session/events"
	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/session"
)

// ActivateAgent admits ordinary API access, recovering a durable Session when
// necessary. It rejects live child Agents and preserves Session RPC errors.
// Success is an access check, not a lease on the Agent's lifetime.
func (owner *Gateway) ActivateAgent(ctx context.Context, identifier session.SessionID) *connection.RPCError {
	_, refused := owner.lifecycle.access.ordinaryAgent(ctx, sessionframes.SessionID(identifier))
	return refused
}
