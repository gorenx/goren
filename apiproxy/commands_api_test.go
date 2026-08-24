package apiproxy

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/gorenx/goren/commands"
	"github.com/gorenx/goren/connection"
)

type commandsAPIFixture struct{}

func (commandsAPIFixture) List(
	context.Context,
	Request[CommandsListRequest],
) (Outcome[[]commands.Descriptor], error) {
	return OK([]commands.Descriptor{
		{
			Name:        "compact",
			Description: "Compact older conversation history",
		},
	}), nil
}

func (commandsAPIFixture) Execute(
	requestContext context.Context,
	call Request[CommandsExecuteRequest],
) (Outcome[*commands.Execution], error) {
	switch call.Payload.Line {
	case "/missing":
		return Absent[*commands.Execution](), nil
	case "/cancel":
		return Outcome[*commands.Execution]{}, requestContext.Err()
	case "/boom":
		return Outcome[*commands.Execution]{}, errors.New("handler exploded")
	default:
		message := "done"
		return OK(&commands.Execution{
			CommandID: "cmd-fixture-1",
			Result: commands.Result{
				Kind: commands.ResultSuccess,
				Text: &message,
			},
		}), nil
	}
}

func TestCommandsRemoteDecodersMatchGeneratedDescriptor(t *testing.T) {
	t.Parallel()
	listRequest, issues := DecodeCommandsListRequest(
		json.RawMessage(`{"args":{"agentId":"agent-1"}}`),
	)
	if len(issues) != 0 || listRequest.AgentID != "agent-1" {
		t.Fatalf("list decode = (%#v, %#v)", listRequest, issues)
	}
	executeRequest, issues := DecodeCommandsExecuteRequest(json.RawMessage(
		`{"args":{"agentId":"agent-1","line":"/compact","images":[{"mediaType":"image/png","data":"AA==","name":"sample.png","ignored":true}]}}`,
	))
	if len(issues) != 0 || executeRequest.AgentID != "agent-1" ||
		executeRequest.Line != "/compact" || len(executeRequest.Images) != 1 ||
		executeRequest.Images[0].Name == nil || *executeRequest.Images[0].Name != "sample.png" {
		t.Fatalf("execute decode = (%#v, %#v)", executeRequest, issues)
	}

	testCases := []struct {
		name     string
		payload  string
		wantPath []string
	}{
		{
			name:     "wrapper must be exact",
			payload:  `{"args":{"agentId":"agent-1"},"extra":true}`,
			wantPath: []string{},
		},
		{
			name:     "images is required",
			payload:  `{"args":{"agentId":"agent-1","line":"/compact"}}`,
			wantPath: []string{"args"},
		},
		{
			name:     "argument set is exact",
			payload:  `{"args":{"agentId":"agent-1","line":"/compact","images":[],"extra":true}}`,
			wantPath: []string{"args"},
		},
		{
			name:     "optional image name rejects null",
			payload:  `{"args":{"agentId":"agent-1","line":"/compact","images":[{"mediaType":"image/png","data":"AA==","name":null}]}}`,
			wantPath: []string{"args", "images", "0", "name"},
		},
		{
			name:     "image media type is closed",
			payload:  `{"args":{"agentId":"agent-1","line":"/compact","images":[{"mediaType":"image/svg+xml","data":"AA=="}]}}`,
			wantPath: []string{"args", "images", "0", "mediaType"},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, decodeIssues := DecodeCommandsExecuteRequest(
				json.RawMessage(testCase.payload),
			)
			if len(decodeIssues) == 0 || !reflect.DeepEqual(decodeIssues[0].Path, testCase.wantPath) {
				t.Fatalf("issues = %#v, want first path %#v", decodeIssues, testCase.wantPath)
			}
		})
	}
}

func TestCommandsRemoteRegistrationPreservesRPCOutcomeSemantics(t *testing.T) {
	t.Parallel()
	methods := NewCatalog()
	if err := RegisterCommandsAPI(methods, commandsAPIFixture{}); err != nil {
		t.Fatal(err)
	}

	listResult := dispatchCommandsFixture(
		t,
		methods,
		context.Background(),
		CommandsListMethod,
		`{"args":{"agentId":"agent-1"}}`,
	)
	if !listResult.OK || listResult.Error != nil || len(listResult.Value) == 0 {
		t.Fatalf("list result = %#v", listResult)
	}

	absentResult := dispatchCommandsFixture(
		t,
		methods,
		context.Background(),
		CommandsExecuteMethod,
		`{"args":{"agentId":"agent-1","line":"/missing","images":[]}}`,
	)
	if !absentResult.OK || absentResult.Error != nil || len(absentResult.Value) != 0 {
		t.Fatalf("absent execution = %#v", absentResult)
	}

	invalidResult := dispatchCommandsFixture(
		t,
		methods,
		context.Background(),
		CommandsExecuteMethod,
		`{"args":{"agentId":"agent-1","line":"/compact"}}`,
	)
	if invalidResult.OK || invalidResult.Error == nil ||
		invalidResult.Error.Code != connection.ErrorInternal ||
		!strings.Contains(invalidResult.Error.Message, "args fields do not match the descriptor") {
		t.Fatalf("invalid Remote input = %#v", invalidResult)
	}

	cancelContext, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()
	cancelledResult := dispatchCommandsFixture(
		t,
		methods,
		cancelContext,
		CommandsExecuteMethod,
		`{"args":{"agentId":"agent-1","line":"/cancel","images":[]}}`,
	)
	if cancelledResult.OK || cancelledResult.Error == nil ||
		cancelledResult.Error.Code != connection.ErrorCancelled ||
		cancelledResult.Error.Message != `Remote invocation "commands/execute" was aborted` {
		t.Fatalf("cancelled Remote invocation = %#v", cancelledResult)
	}

	failedResult := dispatchCommandsFixture(
		t,
		methods,
		context.Background(),
		CommandsExecuteMethod,
		`{"args":{"agentId":"agent-1","line":"/boom","images":[]}}`,
	)
	if failedResult.OK || failedResult.Error == nil ||
		failedResult.Error.Code != connection.ErrorInternal ||
		failedResult.Error.Message != "handler exploded" {
		t.Fatalf("failed Remote invocation = %#v", failedResult)
	}
}

func dispatchCommandsFixture(
	testingContext *testing.T,
	methods *Catalog,
	requestContext context.Context,
	method string,
	payload string,
) connection.RPCResult {
	testingContext.Helper()
	result, err := methods.DispatchUnary(
		requestContext,
		method,
		"commands-fixture",
		json.RawMessage(payload),
	)
	if err != nil {
		testingContext.Fatal(err)
	}
	return result
}

var _ CommandsAPI = commandsAPIFixture{}
