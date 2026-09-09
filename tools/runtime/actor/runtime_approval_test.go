package runtimeactor_test

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/approval"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/toolaskuser"
	"github.com/gorenx/goren/tools"
	toolregistry "github.com/gorenx/goren/tools/registry"
	catalogactor "github.com/gorenx/goren/tools/registry/actor"
	runtimeactor "github.com/gorenx/goren/tools/runtime/actor"
	"github.com/gorenx/goren/tools/tool"
)

// permissionTool controls only the reply and cancellation boundary. This fixture
// proves generic tool routing, not Approval Service audit or Interaction wiring.
type permissionTool struct {
	entered   chan tools.Invocation
	cancelled chan struct{}
	release   chan struct{}
	answer    approval.Outcome
	calls     atomic.Int32
}

func (body *permissionTool) Open(context.Context) (tool.Implementation, error) {
	return tool.Implementation{
		Definition: tools.ToolDefinition{
			Name:       approval.ServiceName,
			Parameters: json.RawMessage(`{"type":"object","properties":{"toolName":{"type":"string"},"callId":{"type":"string"},"reason":{"type":"string"}},"required":["toolName","callId","reason"],"additionalProperties":false}`),
			Executor:   body,
			Output: tools.ToolOutputDefinition{
				Schema:   json.RawMessage(`{"type":"string","enum":["allowed-once","rejected","cancelled","unavailable"]}`),
				Renderer: body,
			},
		},
	}, nil
}

func (body *permissionTool) Execute(_ json.RawMessage, run tools.ToolRunContext) (json.RawMessage, error) {
	body.calls.Add(1)
	body.entered <- run.Execution
	select {
	case <-run.Context.Done():
		close(body.cancelled)
		<-body.release
	case <-body.release:
	}
	// Return even a late allow to verify the original call's cancellation wins.
	return json.Marshal(body.answer)
}

func (*permissionTool) Render(_ json.RawMessage, value json.RawMessage) ([]agentmessage.ContentBlock, error) {
	return []agentmessage.ContentBlock{agentmessage.NewTextBlock(string(value))}, nil
}

type permissionPolicy struct{ calls atomic.Int32 }

func (policy *permissionPolicy) EvaluateCall(context.Context, tools.PreExecuteRequest) (tools.PreExecuteOutcome, error) {
	policy.calls.Add(1)
	return tools.PreExecuteOutcome{
		Decision: tools.AskDecision{
			Reason: "needs permission",
		},
	}, nil
}

func TestRuntimePreparationUsesNamedToolAndWaitsForApprovalExit(t *testing.T) {
	for _, scenario := range []string{"allow", "reject", "cancel"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			body := &permissionTool{
				entered:   make(chan tools.Invocation, 1),
				cancelled: make(chan struct{}),
				release:   make(chan struct{}),
				answer:    approval.OutcomeAllowedOnce,
			}
			defer func() {
				select {
				case <-body.release:
				default:
					close(body.release)
				}
			}()
			if scenario == "reject" {
				body.answer = approval.OutcomeRejected
			}
			policy := &permissionPolicy{}
			system, registry, _ := startCatalog(t, true, tool.Providers{
				toolaskuser.Name:     &source{},
				approval.ServiceName: body,
			})
			permission, err := body.Open(ctx)
			if err != nil {
				t.Fatal(err)
			}
			original, err := toolaskuser.NewDefinition(questions{})
			if err != nil {
				t.Fatal(err)
			}
			hidden := false
			configured := ask[catalogactor.Completion](t, system, registry, catalogactor.RegisterSource{
				Name: "local",
				Catalog: toolregistry.Catalog{
					Schemas: []llm.ToolSchema{
						{
							Name:       original.Name,
							Parameters: original.Parameters,
						},
						{
							Name:       permission.Definition.Name,
							Parameters: permission.Definition.Parameters,
						},
					},
					Policies: map[string]toolregistry.Policy{
						toolaskuser.Name: {
							Before: policy,
						},
						approval.ServiceName: {
							Visible: &hidden,
						},
					},
				},
			})
			if configured.Err != nil {
				t.Fatal(configured.Err)
			}
			target, closing := initializeRuntime(t, system, registry)
			definitions := ask[runtimeactor.Definitions](t, system, target, runtimeactor.ReadDefinitions{
				CreationID: "creation",
			})
			if definitions.Err != nil || len(definitions.Value.Schemas) != 1 || len(definitions.Value.KnownNames) != 2 {
				t.Fatalf("approval visibility: %+v", definitions)
			}
			type preparation struct {
				value runtimeactor.CallResult
				err   error
			}
			completed := make(chan preparation, 1)
			go func() {
				payload, failure := system.AskGrain(ctx, target, runtimeactor.PrepareCall{
					CreationID: "creation",
					Context:    ctx,
					Input: tools.ToolExecutionInput{
						SessionID: "agent",
						CallID:    "original",
						Name:      toolaskuser.Name,
						Arguments: json.RawMessage(`{"questions":[{"id":"q","question":"Continue?"}]}`),
					},
				}, 4*time.Second)
				result, _ := payload.(runtimeactor.CallResult)
				completed <- preparation{
					value: result,
					err:   failure,
				}
			}()
			select {
			case input := <-body.entered:
				var requested approval.Permission
				err = json.Unmarshal(input.Arguments, &requested)
				if err != nil || input.Name != approval.ServiceName || input.SessionID != "agent" || input.CallID == "original" || input.RootCallID != "original" || requested.ToolName != toolaskuser.Name || requested.CallID != "original" {
					t.Fatalf("named approval invocation: %+v, %+v, %v", input, requested, err)
				}
			case result := <-completed:
				t.Fatalf("preparation returned without invoking approval ToolActor: %+v", result)
			case <-ctx.Done():
				t.Fatal("approval ToolActor not called")
			}
			updated := ask[runtimeactor.Definitions](t, system, target, runtimeactor.RefreshConfiguration{
				CreationID: "creation",
				ID:         "during-approval",
			})
			if updated.Err == nil {
				t.Fatal("configuration update crossed the approval wait")
			}
			if scenario == "cancel" {
				acknowledged := ask[runtimeactor.Completion](t, system, target, runtimeactor.CancelCall{
					CreationID: "creation",
					CallID:     "original",
				})
				if acknowledged.Err != nil {
					t.Fatal(acknowledged.Err)
				}
				select {
				case <-body.cancelled:
				case <-ctx.Done():
					t.Fatal("cancellation did not reach the approval ToolActor")
				}
			}
			select {
			case result := <-completed:
				t.Fatalf("preparation returned before approval body exited: %+v", result)
			default:
			}
			close(body.release)
			select {
			case result := <-completed:
				if result.err != nil || result.value.Err != nil {
					t.Fatalf("preparation: %+v", result)
				}
				if (result.value.Stage == tools.ScheduledDispatch) != (scenario == "allow") {
					t.Fatalf("wrong approval result stage: %+v", result)
				}
			case <-ctx.Done():
				t.Fatal("approval result not consumed")
			}
			if scenario == "allow" {
				dispatched := ask[runtimeactor.CallResult](t, system, target, runtimeactor.DispatchCall{
					CreationID: "creation",
					CallID:     "original",
				})
				if dispatched.Err != nil {
					t.Fatal(dispatched.Err)
				}
			}
			final := ask[runtimeactor.CallResult](t, system, target, runtimeactor.FinalizeCall{
				CreationID: "creation",
				CallID:     "original",
			})
			if final.Err != nil || final.Result == nil || final.Result.Failed() != (scenario != "allow") || policy.calls.Load() != 1 || body.calls.Load() != 1 {
				t.Fatalf("result=%+v policy=%d approval=%d", final, policy.calls.Load(), body.calls.Load())
			}
			released := ask[runtimeactor.Completion](t, system, target, runtimeactor.ReleaseCall{
				CreationID: "creation",
				CallID:     "original",
			})
			if released.Err != nil {
				t.Fatal(released.Err)
			}
			closed := ask[runtimeactor.Completion](t, system, target, closing)
			if closed.Err != nil {
				t.Fatal(closed.Err)
			}
		})
	}
}
