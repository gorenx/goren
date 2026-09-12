package mcpclient

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	sdktransport "github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

func TestTypeScriptProtocolFixtureMRTR(t *testing.T) {
	data, err := os.ReadFile("testdata/protocol.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures map[string]json.RawMessage
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	rounds := 0
	fixture := &fixtureTransport{
		responder: func(_ context.Context, request sdktransport.JSONRPCRequest) (*sdktransport.JSONRPCResponse, error) {
			if request.Method == "server/discover" {
				return sdktransport.NewJSONRPCResultResponse(request.ID, fixtures["discover"]), nil
			}
			rounds++
			if rounds == 1 {
				return sdktransport.NewJSONRPCResultResponse(request.ID, fixtures["inputRequired"]), nil
			}
			encoded, _ := json.Marshal(request.Params)
			var params struct {
				RequestState   string                             `json:"requestState"`
				InputResponses map[string]mcp.ElicitationResponse `json:"inputResponses"`
			}
			if err := json.Unmarshal(encoded, &params); err != nil {
				t.Fatal(err)
			}
			if params.RequestState != "typescript-round-state" ||
				params.InputResponses["formInput"].Action != "accept" ||
				params.InputResponses["urlInput"].Action != "accept" {
				t.Fatalf("MRTR did not preserve TypeScript wire identities: %s", encoded)
			}
			return sdktransport.NewJSONRPCResultResponse(request.ID, fixtures["complete"]), nil
		},
	}
	active := startProtocolTestSession(t, fixture, &fixtureElicitor{
		elicit: func(ctx context.Context, request mcp.ElicitationRequest) (*mcp.ElicitationResult, error) {
			if ctx.Value(protocolContextKey{}) != "caller" {
				t.Error("input request lost caller Context")
			}
			var content map[string]any
			if request.Params.Mode == "form" {
				content = map[string]any{"count": 3}
			}
			return elicitationResult("accept", content), nil
		},
	})
	result, err := active.callTool(context.WithValue(t.Context(), protocolContextKey{}, "caller"), "example", json.RawMessage(`{}`))
	if err != nil ||
		string(result) != string(fixtures["complete"]) ||
		rounds != 2 {
		t.Fatalf("TypeScript protocol fixture: %s, %v, rounds %d", result, err, rounds)
	}
}
