package execution

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/approval"
	"github.com/gorenx/goren/tools"
)

func TestPreparationCallsNamedApprovalBeforeOriginalBody(t *testing.T) {
	for _, scenario := range []string{"allowed-once", "rejected", "cancelled", "unavailable", "invalid", "body-failure", "missing", "cancel-beats-allow"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			approvals := 0
			executions := 0
			var captured tools.Invocation
			permission := objectTool(approval.ServiceName, "", executorStub(func(arguments json.RawMessage, run tools.ToolRunContext) (json.RawMessage, error) {
				approvals++
				captured = run.Execution
				var input approval.Permission
				err := json.Unmarshal(arguments, &input)
				if err != nil || input.ToolName != "danger" || input.CallID != "original" || input.Reason != "needs permission" {
					t.Errorf("wrong permission: %+v %v", input, err)
				}
				if scenario == "body-failure" {
					return nil, errors.New("approval failed")
				}
				answer := scenario
				if scenario == "cancel-beats-allow" {
					cancel()
					answer = "allowed-once"
				}
				return json.Marshal(answer)
			}))
			permission.Output.Schema = json.RawMessage(`{"type":"string"}`)
			original := objectTool("danger", "", executorStub(func(arguments json.RawMessage, _ tools.ToolRunContext) (json.RawMessage, error) {
				executions++
				return arguments, nil
			}))
			definitions := []tools.ToolDefinition{original}
			if scenario != "missing" {
				definitions = append(definitions, permission)
			}
			state := newToolsFixture(t, askPolicy("needs permission"))
			coordinator := testScheduler(t, definitions...)
			result := executeCall(t, coordinator, ctx, tools.ToolExecutionInput{
				SessionID:  "conversation",
				CallID:     "original",
				RootCallID: "root",
				Name:       "danger",
				Arguments:  json.RawMessage(`{}`),
			}, state.policies,
			)
			allowed := scenario == "allowed-once"
			if result.Failed() == allowed || executions != boolCount(allowed) {
				t.Fatalf("result=%+v body calls=%d", result, executions)
			}
			if approvals != boolCount(scenario != "missing") {
				t.Fatalf("approval repeated or skipped: %d", approvals)
			}
			if approvals == 1 && (captured.Name != approval.ServiceName || captured.SessionID != "conversation" || captured.CallID == "" || captured.CallID == "original" || captured.RootCallID != "root") {
				t.Fatalf("lost nested invocation identity: %+v", captured)
			}
		})
	}
}

func boolCount(value bool) int {
	if value {
		return 1
	}
	return 0
}

func TestUnconfirmedApprovalCannotDispatchOrFinalizeOriginalCall(t *testing.T) {
	definition := objectTool("danger", "", executorStub(func(json.RawMessage, tools.ToolRunContext) (json.RawMessage, error) {
		t.Fatal("unconfirmed approval executed original body")
		return nil, nil
	}))
	permission := definition.Describe()
	permission.Name = approval.ServiceName
	permission.Output.Schema = json.RawMessage(`{"type":"string"}`)
	failure := errors.New("approval reply lost")
	endpoint := &bodyEndpoint{
		failure: failure,
	}
	state := newToolsFixture(t, askPolicy("permission"))
	coordinator, err := NewScheduler([]tools.Descriptor{definition.Describe(), permission}, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	current, err := coordinator.PrepareCall(context.Background(), tools.ToolExecutionInput{
		SessionID: "conversation",
		CallID:    agentmessage.CallID("original"),
		Name:      "danger",
		Arguments: json.RawMessage(`{}`),
	}, state.policies,
	)
	var delivery *DeliveryError
	if !errors.As(err, &delivery) || !errors.Is(err, failure) || current == nil {
		t.Fatalf("uncertainty lost: %v", err)
	}
	if current.Dispatch() == nil || current.Finalize() == nil {
		t.Fatal("uncertain approval allowed original execution to proceed")
	}
	result, err := current.Result()
	if err == nil || result != nil || endpoint.calls != 1 {
		t.Fatal("approval was repeated or fabricated a final result")
	}
}
