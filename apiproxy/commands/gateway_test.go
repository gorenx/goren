package commandsapi

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gorenx/goren/apiproxy/rpc"
	"github.com/gorenx/goren/commands"
	commandactor "github.com/gorenx/goren/commands/actor"
	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/session"
)

type commandEndpoint struct {
	executions  int
	attachments int
	failure     error
}

func (*commandEndpoint) List(context.Context) ([]commands.Descriptor, error) {
	return []commands.Descriptor{{
		Name: "compact",
	}}, nil
}
func (endpoint *commandEndpoint) Execute(_ context.Context, line string, options commands.ExecuteOptions) (*commands.Execution, error) {
	endpoint.executions++
	endpoint.attachments = options.AttachmentCount
	if line == "/missing" {
		return nil, nil
	}
	if endpoint.failure != nil {
		return nil, endpoint.failure
	}
	return &commands.Execution{
		CommandID: "command",
		Result: commands.Result{
			Kind: commands.ResultSuccess,
		},
	}, nil
}

type commandAccess struct {
	endpoint  Executor
	refused   *connection.RPCError
	requested session.SessionID
}

func (access *commandAccess) ResolveCommands(_ context.Context, id session.SessionID) (Executor, *connection.RPCError) {
	access.requested = id
	return access.endpoint, access.refused
}

func TestGatewayChecksAccessBeforeCommandExecution(t *testing.T) {
	endpoint := &commandEndpoint{}
	access := &commandAccess{
		endpoint: endpoint,
	}
	owner, err := NewGateway(access)
	if err != nil {
		t.Fatal(err)
	}
	methods := rpc.NewCatalog()
	if err := Register(methods, owner); err != nil {
		t.Fatal(err)
	}
	invoke := func(line string) connection.RPCResult {
		t.Helper()
		payload, err := json.Marshal(map[string]any{
			"args": map[string]any{
				"agentId": "conversation",
				"line":    line,
				"images": []Image{{
					MediaType: "image/png",
					Data:      "AA==",
				}},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		result, err := methods.DispatchUnary(context.Background(), ExecuteMethod, "rpc", payload)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	result := invoke("/compact")
	if !result.OK ||
		access.requested != "conversation" ||
		endpoint.executions != 1 ||
		endpoint.attachments != 1 {
		t.Fatalf("execution result = %#v, access = %#v, endpoint = %#v", result, access, endpoint)
	}
	refused := rpc.NewRPCError(connection.ErrorInternal, "access refused", struct{}{})
	access.refused = &refused
	result = invoke("/compact")
	if result.OK ||
		result.Error == nil ||
		result.Error.Message != "access refused" ||
		endpoint.executions != 1 {
		t.Fatalf("refusal was not enforced: %#v", result)
	}
	access.refused = nil
	result = invoke("/missing")
	if !result.OK || len(result.Value) != 0 {
		t.Fatalf("absent result = %#v", result)
	}
	endpoint.failure = errors.New("execution unavailable")
	result = invoke("/compact")
	if result.OK ||
		result.Error == nil ||
		result.Error.Code != connection.ErrorInternal {
		t.Fatalf("failure = %#v", result)
	}
	access.endpoint = nil
	result = invoke("/compact")
	if result.OK || result.Error == nil {
		t.Fatalf("missing capability = %#v", result)
	}
}

var _ Executor = (*commandactor.Client)(nil)
