package execution

import (
	"context"
	"reflect"
	"testing"

	"github.com/gorenx/goren/tools"
)

type panickingPolicy struct{}

func (panickingPolicy) EvaluateCall(context.Context, tools.PreExecuteRequest) (tools.PreExecuteOutcome, error) {
	panic("policy failure")
}
func (panickingPolicy) ReviewResult(context.Context, tools.PostExecuteRequest) (tools.PostExecuteOutcome, error) {
	panic("policy failure")
}
func (panickingPolicy) InterceptExecute(context.Context, tools.ExecuteRequest, tools.ExecuteAction) (tools.ExecuteOutcome, error) {
	panic("policy failure")
}
func (panickingPolicy) RegistryChanged(context.Context) error {
	panic("observer failure")
}

func TestPipelinePanicsReturnFailures(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		options Policies
	}{
		{
			name: "before",
			options: Policies{
				Before: panickingPolicy{},
			},
		},
		{
			name: "after",
			options: Policies{
				After: panickingPolicy{},
			},
		},
		{
			name: "execution",
			options: Policies{
				Execution: panickingPolicy{},
			},
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			business := testScheduler(t, objectTool("echo", "echo", executorStub(passThroughBody)))
			outcome := executeCall(t, business, context.Background(), tools.ToolExecutionInput{
				CallID:    "panic-check",
				Name:      "echo",
				Arguments: []byte(`{}`),
			}, scenario.options,
			)
			if !outcome.Failed() {
				t.Fatal("panic did not produce a failed tool result")
			}
		})
	}
}

type pipelineRecorder struct {
	name  string
	calls *[]string
}

func (recorder pipelineRecorder) InterceptExecute(ctx context.Context, input tools.ExecuteRequest, next tools.ExecuteAction) (tools.ExecuteOutcome, error) {
	*recorder.calls = append(*recorder.calls, recorder.name+":execute")
	return next.Execute(ctx, input)
}

func (recorder pipelineRecorder) ObserveToolResult(context.Context, tools.ExecutionCompleted) error {
	*recorder.calls = append(*recorder.calls, recorder.name+":result")
	return nil
}

func TestPreparedCallRetainsCapturedPolicy(t *testing.T) {
	ctx := context.Background()
	var calls []string
	original := pipelineRecorder{
		name:  "original",
		calls: &calls,
	}
	policies := Policies{
		Execution: original,
		Results:   original,
	}
	definition := objectTool("echo", "echo", executorStub(passThroughBody))
	coordinator := testScheduler(t, definition)
	prepared, err := coordinator.PrepareCall(ctx, tools.ToolExecutionInput{
		CallID:    "old",
		Name:      "echo",
		Arguments: []byte("{}"),
	}, policies,
	)
	if err != nil {
		t.Fatal(err)
	}
	replacement := pipelineRecorder{
		name:  "replacement",
		calls: &calls,
	}
	policies.Execution = replacement
	policies.Results = replacement
	next := coordinator
	err = prepared.Dispatch()
	if err != nil {
		t.Fatal(err)
	}
	err = prepared.Finalize()
	if err != nil {
		t.Fatal(err)
	}
	outcome := executeCall(t, next, ctx, tools.ToolExecutionInput{
		CallID:    "new",
		Name:      "echo",
		Arguments: []byte("{}"),
	}, policies,
	)
	if outcome.Failed() {
		t.Fatal(outcome)
	}
	expected := []string{"original:execute", "original:result", "replacement:execute", "replacement:result"}
	if !reflect.DeepEqual(calls, expected) {
		t.Fatalf("calls = %v", calls)
	}
}
