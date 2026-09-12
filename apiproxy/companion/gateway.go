package companionapi

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	agentregistry "github.com/gorenx/goren/agent/registry"
	"github.com/gorenx/goren/apiproxy/rpc"
	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/settings"
	companions "github.com/gorenx/goren/subagent/companion"
)

// Gateway maps management outcomes to Goren's existing RPC envelope.
type Gateway struct {
	manager Manager
}

// List reports parent preparation separately from the child entries.
func (owner *Gateway) List(ctx context.Context, call rpc.Request[ListRequest]) (rpc.Outcome[Listing], error) {
	value, err := owner.manager.List(ctx, call.Payload)
	return managementOutcome(value, err, call.Payload.ParentSessionID, ""), nil
}

// Create starts a named instance through its current Steward.
func (owner *Gateway) Create(ctx context.Context, call rpc.Request[CreateRequest]) (rpc.Outcome[View], error) {
	value, err := owner.manager.Create(ctx, call.Payload)
	return managementOutcome(value, err, call.Payload.ParentSessionID, call.Payload.Name), nil
}

// Configure changes a saved enablement target at one version.
func (owner *Gateway) Configure(ctx context.Context, call rpc.Request[ConfigureRequest]) (rpc.Outcome[View], error) {
	value, err := owner.manager.Configure(ctx, call.Payload)
	return managementOutcome(value, err, call.Payload.ParentSessionID, call.Payload.Name), nil
}

// Remove retains a tombstone before child closure.
func (owner *Gateway) Remove(ctx context.Context, call rpc.Request[RemoveRequest]) (rpc.Outcome[View], error) {
	value, err := owner.manager.Remove(ctx, call.Payload)
	return managementOutcome(value, err, call.Payload.ParentSessionID, call.Payload.Name), nil
}

// Restart fences the observed child and retains its Session identity.
func (owner *Gateway) Restart(ctx context.Context, call rpc.Request[RestartRequest]) (rpc.Outcome[View], error) {
	value, err := owner.manager.Restart(ctx, call.Payload)
	return managementOutcome(value, err, call.Payload.ParentSessionID, call.Payload.Name), nil
}

func managementOutcome[V any](value V, failure error, parent session.SessionID, name string) rpc.Outcome[V] {
	if failure == nil {
		return rpc.OK(value)
	}
	if errors.Is(failure, context.Canceled) || errors.Is(failure, context.DeadlineExceeded) {
		return rpc.Fail[V](rpc.NewRPCError(connection.ErrorCancelled, "Companion request was cancelled", struct{}{}))
	}
	code := connection.ErrorInternal
	message := "Companion operation failed"
	switch {
	case errors.Is(failure, ErrNotFound):
		code = connection.ErrorCompanionNotFound
		message = "Companion policy or instance was not found"
	case errors.Is(failure, companions.ErrInvalid), errors.Is(failure, settings.ErrInvalid):
		code = connection.ErrorCompanionInvalid
		message = "Companion configuration or request is invalid"
	case errors.Is(failure, agent.ErrRole):
		code = connection.ErrorCompanionForbidden
		message = "Companion role is not allowed for this operation"
	case errors.Is(failure, companions.ErrConflict), errors.Is(failure, settings.ErrConflict):
		code = connection.ErrorCompanionConflict
		message = "Companion management revision conflicts"
	case errors.Is(failure, agentregistry.ErrCreation), errors.Is(failure, agent.ErrClosing), errors.Is(failure, agent.ErrBusy):
		code = connection.ErrorCompanionUnavailable
		message = "Parent or Companion is unavailable"
	}
	if code == connection.ErrorInternal {
		return rpc.Fail[V](rpc.NewRPCError(code, message, struct{}{}))
	}
	if code == connection.ErrorCompanionForbidden {
		return rpc.Fail[V](rpc.NewRPCError(code, message, struct {
			ParentSessionID session.SessionID `json:"parentSessionId"`
		}{
			ParentSessionID: parent,
		}))
	}
	return rpc.Fail[V](rpc.NewRPCError(code, message, struct {
		ParentSessionID session.SessionID `json:"parentSessionId"`
		Name            string            `json:"name"`
	}{
		ParentSessionID: parent,
		Name:            name,
	}))
}
