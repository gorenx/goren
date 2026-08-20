package plugin

import (
	"context"
	"reflect"
	"testing"
)

type runtimeTestEvent struct {
	EventBase
	Value string
}

var runtimeTestEventDefinition = DefineEvent[runtimeTestEvent](
	"test/event",
	DeliveryOrdered,
)

type runtimeTestObserver struct {
	name  string
	trace *[]string
}

func (observer *runtimeTestObserver) ObserveEvent(
	_ context.Context,
	_ runtimeTestEvent,
) error {
	if observer.trace != nil {
		*observer.trace = append(*observer.trace, observer.name)
	}
	return nil
}

func TestEventRoutesCurrentScopeToRoot(t *testing.T) {
	t.Parallel()
	runtimeEngine := NewRuntime(RuntimeSettings{})
	trace := make([]string, 0)
	var rootScope *Scope
	var tenantScope *Scope
	_, err := runtimeEngine.Load(
		context.Background(),
		&runtimeTestPlugin{
			metadata: Manifest{
				Name: "event-observers",
			},
			applyOperation: func(_ context.Context, pluginContext *Context) error {
				rootScope = pluginContext.Scope()
				if observeErr := runtimeTestEventDefinition.Observe(
					pluginContext,
					&runtimeTestObserver{
						name:  "root",
						trace: &trace,
					},
				); observeErr != nil {
					return observeErr
				}
				childContext, childErr := pluginContext.ChildScope("tenant")
				if childErr != nil {
					return childErr
				}
				tenantScope = childContext.Scope()
				return runtimeTestEventDefinition.Observe(
					childContext,
					&runtimeTestObserver{
						name:  "child",
						trace: &trace,
					},
				)
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtimeTestEventDefinition.Publish(
		context.Background(),
		tenantScope,
		runtimeTestEvent{
			Value: "child",
		},
	); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(trace, []string{"child", "root"}) {
		t.Fatalf("child Event trace = %v", trace)
	}
	trace = trace[:0]
	if err := runtimeTestEventDefinition.Publish(
		context.Background(),
		rootScope,
		runtimeTestEvent{
			Value: "root",
		},
	); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(trace, []string{"root"}) {
		t.Fatalf("root Event leaked into child Scope: %v", trace)
	}
}
