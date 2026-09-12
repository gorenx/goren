// Package mcpapi owns the Goren browser MCP management boundary, not MCP wire DTOs.
package mcpapi

import (
	"context"
	"encoding/json"

	"github.com/gorenx/goren/apiproxy/rpc"
	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/session"
)

// ListRequest selects an existing Session's connection state.
type ListRequest struct {
	SessionID session.SessionID `json:"sessionId"`
}

// RetryRequest restores one configured target in an existing Session.
type RetryRequest struct {
	SessionID  session.SessionID `json:"sessionId"`
	ServerName string            `json:"serverName"`
}

// View contains non-secret, detached management state.
type View struct {
	ServerName string `json:"serverName"`
	Transport  string `json:"transport"`
	Status     string `json:"status"`
	ToolCount  int    `json:"toolCount"`
}

// Directory is the consumer-owned port for Session MCP state and recovery.
type Directory interface {
	ListConnections(context.Context, session.SessionID) ([]View, error)
	RetryConnection(context.Context, RetryRequest) error
}

// Register adds Session-scoped MCP management to the existing RPC carrier.
func Register(methods *rpc.Catalog, source Directory) error {
	if source == nil {
		return nil
	}
	if err := rpc.RegisterUnary(methods, "mcp.list", decodeList, func(ctx context.Context, input rpc.Request[ListRequest]) (rpc.Outcome[[]View], error) {
		value, err := source.ListConnections(ctx, input.Payload.SessionID)
		return managementOutcome(value, err), nil
	}); err != nil {
		return err
	}
	return rpc.RegisterUnary(methods, "mcp.retry", decodeRetry, func(ctx context.Context, input rpc.Request[RetryRequest]) (rpc.Outcome[struct{}], error) {
		return managementOutcome(struct{}{}, source.RetryConnection(ctx, input.Payload)), nil
	})
}

func managementOutcome[V any](value V, err error) rpc.Outcome[V] {
	if err != nil {
		// Transport errors can include URLs or remote data; never echo those.
		return rpc.Fail[V](rpc.NewRPCError(connection.ErrorInternal, "MCP operation failed; check the endpoint, protocol version and server name", struct{}{}))
	}
	return rpc.OK(value)
}

func decodeRetry(raw json.RawMessage) (RetryRequest, []connection.ValidationIssue) {
	fields, issues := managementFields(raw, "sessionId", "serverName")
	identifier, identityIssues := rpc.RequiredString(fields, "sessionId", true)
	name, nameIssues := rpc.RequiredString(fields, "serverName", true)
	issues = append(issues, identityIssues...)
	return RetryRequest{
		SessionID:  session.SessionID(identifier),
		ServerName: name,
	}, append(issues, nameIssues...)
}

func decodeList(raw json.RawMessage) (ListRequest, []connection.ValidationIssue) {
	fields, issues := managementFields(raw, "sessionId")
	identifier, identityIssues := rpc.RequiredString(fields, "sessionId", true)
	return ListRequest{
		SessionID: session.SessionID(identifier),
	}, append(issues, identityIssues...)
}

func managementFields(raw json.RawMessage, allowed ...string) (map[string]json.RawMessage, []connection.ValidationIssue) {
	fields, issues := rpc.ObjectFields(raw)
	for name := range fields {
		known := false
		for _, candidate := range allowed {
			known = known || candidate == name
		}
		if !known {
			issues = append(issues, connection.ValidationIssue{
				Code:    "unrecognized_key",
				Path:    []string{name},
				Message: "Unknown MCP management field",
			})
		}
	}
	return fields, issues
}
