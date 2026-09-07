package tools_test

import (
	"context"
	"sync"
	"testing"

	"github.com/gorenx/goren/approval"
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

func (policy beforePolicy) ResolvePreExecute(ctx context.Context, input tools.PreExecuteRequest) (tools.PreExecuteOutcome, error) {
	return policy.source.Intercept(ctx, input, defaultBefore{})
}

type afterPolicy struct {
	source middleware[tools.PostExecuteRequest, tools.PostExecuteOutcome]
}

func (policy afterPolicy) ResolvePostExecute(ctx context.Context, input tools.PostExecuteRequest) (tools.PostExecuteOutcome, error) {
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

func (recorder *eventRecorder) RegistryChanged(ctx context.Context) error {
	return recorder.observe(ctx, tools.RegistryChanged{})
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
	service  *tools.ToolLayer
	failures *eventRecorder
}

func newToolsFixture(t *testing.T, dependencies ...any) *toolsFixture {
	t.Helper()
	settings, err := tools.ValidateConfig(tools.Config{})
	if err != nil {
		t.Fatal(err)
	}
	recorder := &eventRecorder{}
	options := tools.Options{
		Results: recorder,
		Changes: recorder,
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
		case approval.Approval:
			options.Approvals = selected
		default:
			t.Fatalf("unsupported test dependency: %T", dependency)
		}
	}
	business, err := tools.New(settings, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := business.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return &toolsFixture{
		service:  business,
		failures: recorder,
	}
}
