package sessionapi

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"

	sessionframes "github.com/gorenx/goren/apiproxy/session/events"

	rpc "github.com/gorenx/goren/apiproxy/rpc"
	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/session"

	sesspersist "github.com/gorenx/goren/session/persistence"
)

type sessionAccess struct {
	runtimeSessions *activation
}

func (access *sessionAccess) ordinaryAgent(
	requestContext context.Context,
	identifier sessionframes.SessionID,
) (Agent, *connection.RPCError) {
	subject, err := access.runtimeSessions.ordinary(requestContext, session.SessionID(identifier))
	if err == nil {
		return subject, nil
	}
	var missing *sesspersist.NotFoundError
	if errors.As(err, &missing) {
		problem := sessionNotFoundError(identifier)
		return nil, &problem
	}
	var ownership *SubagentOwnershipError
	if errors.As(err, &ownership) {
		problem := subagentOwnershipError(identifier)
		return nil, &problem
	}
	problem := rpc.NewRPCError(connection.ErrorInternal, err.Error(), struct{}{})
	return nil, &problem
}

func sessionNotFoundError(identifier sessionframes.SessionID) connection.RPCError {
	return rpc.NewRPCError(
		connection.ErrorSessionNotFound,
		fmt.Sprintf("session %q not found (not attached)", identifier),
		struct {
			SessionID sessionframes.SessionID `json:"sessionId"`
		}{
			SessionID: identifier,
		},
	)
}

func subagentOwnershipError(identifier sessionframes.SessionID) connection.RPCError {
	return rpc.NewRPCError(
		connection.ErrorAgentBusy,
		fmt.Sprintf("session %q is owned by subagent routing", identifier),
		struct {
			Reason string `json:"reason"`
		}{
			Reason: "use subagent delivery for this child session",
		},
	)
}

func mintSessionID() (session.SessionID, error) {
	randomID, err := mintUUID()
	if err != nil {
		return "", err
	}
	return session.SessionID("session-" + randomID), nil
}

func mintUUID() (string, error) {
	var randomBytes [16]byte
	if _, err := rand.Read(randomBytes[:]); err != nil {
		return "", err
	}
	randomBytes[6] = randomBytes[6]&0x0f | 0x40
	randomBytes[8] = randomBytes[8]&0x3f | 0x80
	return fmt.Sprintf(
		"%x-%x-%x-%x-%x",
		randomBytes[0:4], randomBytes[4:6], randomBytes[6:8], randomBytes[8:10], randomBytes[10:16],
	), nil
}

// attachedAgent resolves live control requests without recovering durable Sessions.
func (access *sessionAccess) attachedAgent(ctx context.Context, identifier sessionframes.SessionID) (Agent, *connection.RPCError) {
	subject, err := access.runtimeSessions.agents.Resolve(ctx, session.SessionID(identifier))
	if err != nil {
		problem := rpc.NewRPCError(connection.ErrorInternal, err.Error(), struct{}{})
		return nil, &problem
	}
	if subject == nil {
		problem := sessionNotFoundError(identifier)
		return nil, &problem
	}
	if subject.ID() != session.SessionID(identifier) {
		problem := rpc.NewRPCError(connection.ErrorInternal, "Agent resolution returned another identity", struct{}{})
		return nil, &problem
	}
	if subject.ParentID() != nil {
		problem := subagentOwnershipError(identifier)
		return nil, &problem
	}
	return subject, nil
}
