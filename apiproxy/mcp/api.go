// Package mcpapi owns the Goren browser MCP management boundary, not MCP wire DTOs.
package mcpapi

import (
	"context"
	"encoding/json"

	"github.com/gorenx/goren/apiproxy/rpc"
	"github.com/gorenx/goren/connection"
)

// ConnectRequest permits only a named HTTP endpoint, never subprocess execution.
type ConnectRequest struct {
	ServerName string `json:"serverName"`
	URL        string `json:"url"`
}

// DisconnectRequest identifies one process-local connection.
type DisconnectRequest struct {
	ServerName string `json:"serverName"`
}

// View contains non-secret, detached management state.
type View struct {
	ServerName string `json:"serverName"`
	Transport  string `json:"transport"`
	Status     string `json:"status"`
	ToolCount  int    `json:"toolCount"`
}

// Directory is the consumer-owned process MCP management port.
type Directory interface {
	ConnectHTTP(context.Context, ConnectRequest) (View, error)
	ListConnections(context.Context) ([]View, error)
	Disconnect(context.Context, string) error
}

// Register adds the explicitly scoped Goren extension to the existing RPC carrier.
func Register(methods *rpc.Catalog, source Directory) error {
	if source == nil {
		return nil
	}
	if err := rpc.RegisterUnary(methods, "mcp.connect", decodeConnect, func(ctx context.Context, input rpc.Request[ConnectRequest]) (rpc.Outcome[View], error) {
		value, err := source.ConnectHTTP(ctx, input.Payload)
		return managementOutcome(value, err), nil
	}); err != nil {
		return err
	}
	if err := rpc.RegisterUnary(methods, "mcp.list", decodeList, func(ctx context.Context, _ rpc.Request[struct{}]) (rpc.Outcome[[]View], error) {
		value, err := source.ListConnections(ctx)
		return managementOutcome(value, err), nil
	}); err != nil {
		return err
	}
	return rpc.RegisterUnary(methods, "mcp.disconnect", decodeDisconnect, func(ctx context.Context, input rpc.Request[DisconnectRequest]) (rpc.Outcome[struct{}], error) {
		return managementOutcome(struct{}{}, source.Disconnect(ctx, input.Payload.ServerName)), nil
	})
}

func managementOutcome[V any](value V, err error) rpc.Outcome[V] {
	if err != nil {
		// Transport errors can include URLs or remote data; never echo those.
		return rpc.Fail[V](rpc.NewRPCError(connection.ErrorInternal, "MCP operation failed; check the endpoint, protocol version and server name", struct{}{}))
	}
	return rpc.OK(value)
}

func decodeConnect(raw json.RawMessage) (ConnectRequest, []connection.ValidationIssue) {
	fields, issues := managementFields(raw, "serverName", "url")
	name, nameIssues := rpc.RequiredString(fields, "serverName", true)
	address, addressIssues := rpc.RequiredString(fields, "url", true)
	issues = append(issues, nameIssues...)
	issues = append(issues, addressIssues...)
	return ConnectRequest{
		ServerName: name,
		URL:        address,
	}, issues
}

func decodeDisconnect(raw json.RawMessage) (DisconnectRequest, []connection.ValidationIssue) {
	fields, issues := managementFields(raw, "serverName")
	name, nameIssues := rpc.RequiredString(fields, "serverName", true)
	return DisconnectRequest{
		ServerName: name,
	}, append(issues, nameIssues...)
}

func decodeList(raw json.RawMessage) (struct{}, []connection.ValidationIssue) {
	_, issues := managementFields(raw)
	return struct{}{}, issues
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
