package execution

import (
	"context"
	"sync"
	"testing"

	"github.com/gorenx/goren/tools"
)

type action[I, O any] interface {
	Execute(context.Context, I) (O, error)
}

type middleware[I, O any] interface {
	Intercept(context.Context, I, action[I, O]) (O, error)
}

type defaultBefore struct{}

func (defaultBefore) Execute(context.Context, tools.PreExecuteRequest) (tools.PreExecuteOutcome, error) {
	return tools.PreExecuteOutcome{
		Decision: tools.AllowDecision{},
	}, nil
}

type defaultAfter struct{}

func (defaultAfter) Execute(context.Context, tools.PostExecuteRequest) (tools.PostExecuteOutcome, error) {
	return tools.PostExecuteOutcome{
		Decision: tools.AcceptDecision{},
	}, nil
}

type beforePolicy struct {
	source middleware[tools.PreExecuteRequest, tools.PreExecuteOutcome]
}

func (policy beforePolicy) EvaluateCall(ctx context.Context, input tools.PreExecuteRequest) (tools.PreExecuteOutcome, error) {
	return policy.source.Intercept(ctx, input, defaultBefore{})
}

type afterPolicy struct {
	source middleware[tools.PostExecuteRequest, tools.PostExecuteOutcome]
}

func (policy afterPolicy) ReviewResult(ctx context.Context, input tools.PostExecuteRequest) (tools.PostExecuteOutcome, error) {
	return policy.source.Intercept(ctx, input, defaultAfter{})
}

type bodyPolicy struct {
	source middleware[tools.ExecuteRequest, tools.ExecuteOutcome]
}

func (policy bodyPolicy) InterceptExecute(ctx context.Context, input tools.ExecuteRequest, downstream tools.ExecuteAction) (tools.ExecuteOutcome, error) {
	return policy.source.Intercept(ctx, input, downstream)
}

type toolEvent interface {
	EventName() string
}

type eventObserver struct {
	name          string
	subscriptions []string
	observe       func(context.Context, toolEvent) error
}

type observationFailure struct {
	EventName string
	Error     error
}

type eventRecorder struct {
	observers []*eventObserver
	mutex     sync.Mutex
	failures  []observationFailure
}

func (recorder *eventRecorder) ObserveToolResult(ctx context.Context, completed tools.ExecutionCompleted) error {
	return recorder.observe(ctx, completed)
}
func (recorder *eventRecorder) observe(ctx context.Context, fact toolEvent) error {
	for _, observer := range recorder.observers {
		for _, name := range observer.subscriptions {
			if name != fact.EventName() {
				continue
			}
			if err := observer.observe(ctx, fact); err != nil {
				recorder.mutex.Lock()
				recorder.failures = append(recorder.failures, observationFailure{
					EventName: name,
					Error:     err,
				})
				recorder.mutex.Unlock()
				return err
			}
		}
	}
	return nil
}

type toolsFixture struct {
	policies Policies
	failures *eventRecorder
}

func newToolsFixture(t *testing.T, dependencies ...any) *toolsFixture {
	t.Helper()
	recorder := &eventRecorder{}
	options := Policies{
		Results: recorder,
	}
	for _, dependency := range dependencies {
		switch selected := dependency.(type) {
		case *policyFixture[tools.PreExecuteRequest, tools.PreExecuteOutcome]:
			options.Before = beforePolicy{
				source: selected,
			}
		case *policyFixture[tools.PostExecuteRequest, tools.PostExecuteOutcome]:
			options.After = afterPolicy{
				source: selected,
			}
		case *policyFixture[tools.ExecuteRequest, tools.ExecuteOutcome]:
			options.Execution = bodyPolicy{
				source: selected,
			}
		case *eventObserver:
			recorder.observers = append(recorder.observers, selected)
		default:
			t.Fatalf("unsupported test dependency: %T", dependency)
		}
	}
	return &toolsFixture{
		policies: options,
		failures: recorder,
	}
}

// directBodies is a test delivery endpoint; scheduling uses the production Scheduler.
type directBodies map[string]tools.Executor

func (bodies directBodies) Invoke(ctx context.Context, input tools.Invocation) (tools.BodyResult, error) {
	return input.Run(ctx, bodies[input.Name]), nil
}
func testScheduler(t *testing.T, definitions ...tools.ToolDefinition) *Scheduler {
	t.Helper()
	descriptions := make([]tools.Descriptor, 0, len(definitions))
	bodies := make(directBodies, len(definitions))
	for _, definition := range definitions {
		descriptions = append(descriptions, definition.Describe())
		bodies[definition.Name] = definition.Executor
	}
	coordinator, err := NewScheduler(descriptions, bodies)
	if err != nil {
		t.Fatal(err)
	}
	return coordinator
}

// executeCall drives the production Call protocol for tests with no interactive approver.
func executeCall(t *testing.T, coordinator *Scheduler, ctx context.Context, input tools.ToolExecutionInput, policies Policies) tools.ToolExecutionResult {
	t.Helper()
	current, err := coordinator.PrepareCall(ctx, input, policies)
	if err != nil {
		t.Fatal(err)
	}
	phase := current.Preparation()
	if phase == tools.ScheduledDispatch {
		err = current.Dispatch()
		if err != nil {
			t.Fatal(err)
		}
	}
	err = current.Finalize()
	if err != nil {
		t.Fatal(err)
	}
	result, err := current.Result()
	if err != nil {
		t.Fatal(err)
	}
	return result
}
