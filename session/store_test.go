package session

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/gorenx/goren/plugin"
)

type storeProviderPlugin struct {
	registry *MemoryStore
	reporter func(error)
}

func (instance *storeProviderPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{Name: "fixture-session-provider", Provides: []plugin.ServiceRef{StoreService.Ref()}}
}

func (instance *storeProviderPlugin) Apply(requestContext context.Context, pluginScope *plugin.Scope) error {
	registry, err := NewMemoryStore(pluginScope, MemoryStoreOptions{ObserverError: instance.reporter})
	if err != nil {
		return err
	}
	instance.registry = registry
	if err := pluginScope.Effect(requestContext, "sessions", func(context.Context) (plugin.Disposer, error) {
		return registry.Close, nil
	}); err != nil {
		return err
	}
	_, err = plugin.Provide(pluginScope, StoreService, Store(registry))
	return err
}

type storeConsumerPlugin struct {
	name  string
	body  func(*plugin.Scope, Store) error
	scope *plugin.Scope
}

func (instance *storeConsumerPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{Name: instance.name, Requires: []plugin.ServiceRef{StoreService.Ref()}}
}

func (instance *storeConsumerPlugin) Apply(_ context.Context, pluginScope *plugin.Scope) error {
	service, found := plugin.Require(pluginScope, StoreService)
	if !found {
		return errors.New("fixture: sessions service missing")
	}
	instance.scope = pluginScope
	if instance.body == nil {
		return nil
	}
	return instance.body(pluginScope, service)
}

func TestStoreLifecyclePublishesCommittedEventsAndFlush(t *testing.T) {
	t.Parallel()
	requestContext := context.Background()
	engine := plugin.NewRuntime()
	calls := []string{}
	consumer := &storeConsumerPlugin{name: "fixture-session-consumer"}
	consumer.body = func(pluginScope *plugin.Scope, _ Store) error {
		if _, err := OnCreated(pluginScope, func(context.Context, *Session) error {
			calls = append(calls, "created")
			return nil
		}); err != nil {
			return err
		}
		if _, err := OnEvent(pluginScope, func(_ context.Context, activeSession *Session, committed Event) error {
			if activeSession.Seq() != committed.Seq+1 {
				return errors.New("event was published before commit")
			}
			calls = append(calls, "event")
			return nil
		}); err != nil {
			return err
		}
		if _, err := OnFlush(pluginScope, func(context.Context, *Session) error {
			calls = append(calls, "flush")
			return nil
		}); err != nil {
			return err
		}
		_, err := OnDisposed(pluginScope, func(context.Context, *Session) error {
			calls = append(calls, "disposed")
			return nil
		})
		return err
	}
	consumerHandle, err := engine.Load(requestContext, consumer)
	if err != nil {
		t.Fatal(err)
	}
	provider := &storeProviderPlugin{}
	if _, err := engine.Load(requestContext, provider); err != nil {
		t.Fatal(err)
	}
	conversation, err := provider.registry.Create(requestContext, consumer.scope, nil, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Append(conversation, fixtureEventKey, fixturePayload{Items: []string{"value"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.registry.Flush(requestContext, conversation); err != nil {
		t.Fatal(err)
	}
	if err := engine.Unload(requestContext, consumerHandle); err != nil {
		t.Fatal(err)
	}
	if _, found := provider.registry.Get(conversation.ID()); found {
		t.Fatal("Session remained live after owner scope unload")
	}
	if want := []string{"created", "event", "flush", "disposed"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestCreationFailureRollsBackWithPairedDisposal(t *testing.T) {
	t.Parallel()
	requestContext := context.Background()
	engine := plugin.NewRuntime()
	sentinel := errors.New("creation veto")
	calls := []string{}
	consumer := &storeConsumerPlugin{name: "fixture-veto-consumer"}
	consumer.body = func(pluginScope *plugin.Scope, _ Store) error {
		if _, err := OnCreated(pluginScope, func(context.Context, *Session) error {
			calls = append(calls, "created")
			return sentinel
		}); err != nil {
			return err
		}
		_, err := OnDisposed(pluginScope, func(context.Context, *Session) error {
			calls = append(calls, "disposed")
			return nil
		})
		return err
	}
	if _, err := engine.Load(requestContext, consumer); err != nil {
		t.Fatal(err)
	}
	provider := &storeProviderPlugin{}
	if _, err := engine.Load(requestContext, provider); err != nil {
		t.Fatal(err)
	}
	identifier := SessionID("vetoed")
	if _, err := provider.registry.Create(requestContext, consumer.scope, &identifier, CreateOptions{}); !errors.Is(err, sentinel) {
		t.Fatalf("create error = %v", err)
	}
	if _, found := provider.registry.Get(identifier); found {
		t.Fatal("vetoed Session remained in Store")
	}
	if want := []string{"created", "disposed"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestAppendObserverFailureIsContainedAndReentryRejected(t *testing.T) {
	t.Parallel()
	requestContext := context.Background()
	engine := plugin.NewRuntime()
	var reportsMu sync.Mutex
	reports := []error{}
	provider := &storeProviderPlugin{reporter: func(observerErr error) {
		reportsMu.Lock()
		reports = append(reports, observerErr)
		reportsMu.Unlock()
	}}
	consumer := &storeConsumerPlugin{name: "fixture-reentrant-consumer"}
	consumer.body = func(pluginScope *plugin.Scope, _ Store) error {
		_, err := OnEvent(pluginScope, func(_ context.Context, activeSession *Session, _ Event) error {
			_, appendErr := Append(activeSession, fixtureEventKey, fixturePayload{Items: []string{"nested"}})
			if appendErr == nil || !strings.Contains(appendErr.Error(), "cannot reenter") {
				return errors.New("nested append was not rejected")
			}
			return errors.New("observer failure")
		})
		return err
	}
	if _, err := engine.Load(requestContext, consumer); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Load(requestContext, provider); err != nil {
		t.Fatal(err)
	}
	conversation, err := provider.registry.Create(requestContext, consumer.scope, nil, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	committed, err := Append(conversation, fixtureEventKey, fixturePayload{Items: []string{"outer"}})
	if err != nil {
		t.Fatal(err)
	}
	if committed.Seq != 0 || conversation.Seq() != 1 {
		t.Fatalf("committed seq = %d, next seq = %d", committed.Seq, conversation.Seq())
	}
	reportsMu.Lock()
	defer reportsMu.Unlock()
	if len(reports) != 1 || !strings.Contains(reports[0].Error(), "observer failure") {
		t.Fatalf("observer reports = %#v", reports)
	}
}
