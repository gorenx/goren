package mcpclient

import (
	"context"
	"encoding/json"
	"testing"

	sdktransport "github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

func TestRefreshRetainsCompleteCatalogOnInvalidPages(t *testing.T) {
	for _, invalid := range []string{
		`{"resultType":"complete","tools":[{"name":"duplicated","inputSchema":{"type":"object"}},{"name":"duplicated","inputSchema":{"type":"object"}}]}`,
		`{"resultType":"complete","tools":[],"nextCursor":"same"}`,
		`{"resultType":"complete","tools":[{"name":"bad","inputSchema":{"type":"string"}}]}`,
		`{"tools":[]}`,
	} {
		t.Run(invalid, func(t *testing.T) {
			response := `{"resultType":"complete","tools":[{"name":"original","inputSchema":{"type":"object"}}]}`
			fixture := &fixtureTransport{
				responder: func(_ context.Context, request sdktransport.JSONRPCRequest) (*sdktransport.JSONRPCResponse, error) {
					if request.Method == "server/discover" {
						return discoverResponse(request.ID), nil
					}
					return sdktransport.NewJSONRPCResultResponse(request.ID, json.RawMessage(response)), nil
				},
			}
			active := startProtocolTestSession(t, fixture, nil)
			owner := &transport{
				session:  active,
				lifetime: t.Context(),
				settings: Config{
					ServerName: "fixture",
					MaxPages:   3,
					MaxTools:   10,
				},
			}
			if err := owner.Refresh(t.Context()); err != nil {
				t.Fatal(err)
			}
			response = invalid
			if err := owner.Refresh(t.Context()); err == nil {
				t.Fatal("invalid refresh succeeded")
			}
			if entries := owner.Schemas(); len(entries) != 1 || entries[0].Name != ToolName("fixture", "original") {
				t.Fatal("last complete catalog was lost")
			}
		})
	}
}

func TestProtocolDiscoveryRejectsIncompatibleSuccess(t *testing.T) {
	fixture := &fixtureTransport{
		responder: func(_ context.Context, request sdktransport.JSONRPCRequest) (*sdktransport.JSONRPCResponse, error) {
			if request.Method != "server/discover" {
				t.Fatalf("legacy request reached wire: %s", request.Method)
			}
			return sdktransport.NewJSONRPCResultResponse(mcp.NewRequestId(1), json.RawMessage(`{"resultType":"complete","supportedVersions":["2025-11-25"],"capabilities":{}}`)), nil
		},
	}
	active, err := newProtocolSession(fixture, protocolTestSettings(nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := active.start(t.Context()); err == nil {
		t.Fatal("wrong version accepted")
	}
}
