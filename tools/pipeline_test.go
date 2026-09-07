package tools_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/gorenx/goren/tools"
)

type panickingPolicy struct{}

func (panickingPolicy) ResolvePreExecute(context.Context, tools.PreExecuteRequest) (tools.PreExecuteOutcome, error) {
	panic("policy failure")
}
func (panickingPolicy) ResolvePostExecute(context.Context, tools.PostExecuteRequest) (tools.PostExecuteOutcome, error) {
	panic("policy failure")
}
func (panickingPolicy) InterceptExecute(context.Context, tools.ExecuteRequest, tools.ExecuteAction) (tools.ExecuteOutcome, error) {
	panic("policy failure")
}
func (panickingPolicy) RegistryChanged(context.Context) error {
	panic("observer failure")
}

func TestPipelinePanicsReturnFailures(t *testing.T) {
	settings, err := tools.ValidateConfig(tools.Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []struct {
		name    string
		options tools.Options
	}{
		{
			name: "before",
			options: tools.Options{
				Before: panickingPolicy{},
			},
		},
		{
			name: "after",
			options: tools.Options{
				After: panickingPolicy{},
			},
		},
		{
			name: "execution",
			options: tools.Options{
				Execution: panickingPolicy{},
			},
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			business, err := tools.New(settings, scenario.options)
			if err != nil {
				t.Fatal(err)
			}
			defer business.Close(context.Background())
			if _, err := business.AddTool(context.Background(), objectTool("echo", "echo", executorStub(passThroughBody))); err != nil {
				t.Fatal(err)
			}
			outcome := business.Execute(context.Background(), tools.ToolExecutionInput{
				CallID:    "panic-check",
				Name:      "echo",
				Arguments: []byte(`{}`),
			})
			if !outcome.Failed() {
				t.Fatal("panic did not produce a failed tool result")
			}
		})
	}
	business, err := tools.New(settings, tools.Options{
		Changes: panickingPolicy{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer business.Close(context.Background())
	if _, err := business.AddTool(context.Background(), objectTool("echo", "echo", executorStub(passThroughBody))); err == nil {
		t.Fatal("observer panic was not returned")
	}
	if _, exists := business.Get("echo"); exists {
		t.Fatal("failed registration survived observer panic")
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

func TestPreparedCallRetainsPipelineAfterRegistrationChanges(t *testing.T) {
	fixture := newToolsFixture(t)
	business := fixture.service
	ctx := context.Background()
	if _, err := business.AddTool(ctx, objectTool("echo", "echo", executorStub(passThroughBody))); err != nil {
		t.Fatal(err)
	}
	var calls []string
	original := pipelineRecorder{
		name:  "original",
		calls: &calls,
	}
	executionID, err := business.UseExecution(original)
	if err != nil {
		t.Fatal(err)
	}
	observerID, err := business.ObserveResults(original)
	if err != nil {
		t.Fatal(err)
	}
	scheduler := business.Scheduler()
	prepared, err := scheduler.Prepare(ctx, tools.ToolExecutionInput{
		CallID:    "old",
		Name:      "echo",
		Arguments: []byte("{}"),
	})
	if err != nil || prepared.Stage != tools.ScheduledDispatch {
		t.Fatalf("prepare = %#v, %v", prepared, err)
	}
	for _, identifier := range []tools.RegistrationID{executionID, observerID} {
		if removed, err := business.RemoveRegistration(ctx, identifier); err != nil || !removed {
			t.Fatalf("remove = %v, %v", removed, err)
		}
		if removed, err := business.RemoveRegistration(ctx, identifier); err != nil || removed {
			t.Fatalf("duplicate remove = %v, %v", removed, err)
		}
	}
	replacement := pipelineRecorder{
		name:  "replacement",
		calls: &calls,
	}
	if _, err := business.UseExecution(replacement); err != nil {
		t.Fatal(err)
	}
	replacementID, err := business.ObserveResults(replacement)
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if removed, err := business.RemoveRegistration(canceled, replacementID); removed || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled remove = %v, %v", removed, err)
	}
	dispatched, err := scheduler.Dispatch(prepared.Execution)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := scheduler.Finalize(prepared.Execution, dispatched.Result)
	if err != nil || outcome.Failed() {
		t.Fatalf("old call = %#v, %v", outcome, err)
	}
	outcome = business.Execute(ctx, tools.ToolExecutionInput{
		CallID:    "new",
		Name:      "echo",
		Arguments: []byte("{}"),
	})
	if outcome.Failed() {
		t.Fatalf("new call = %#v", outcome)
	}
	expected := []string{"original:execute", "original:result", "replacement:execute", "replacement:result"}
	if !reflect.DeepEqual(calls, expected) {
		t.Fatalf("calls = %v, want %v", calls, expected)
	}
	if err := business.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := business.UseExecution(original); !errors.Is(err, tools.ErrToolLayerInactive) {
		t.Fatalf("register after close = %v", err)
	}
	if _, err := business.ObserveResults(original); !errors.Is(err, tools.ErrToolLayerInactive) {
		t.Fatalf("observe after close = %v", err)
	}
}
