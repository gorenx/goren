package llmactor_test

import (
	"context"
	"testing"
	"time"

	"github.com/gorenx/goren/llm"
	llmactor "github.com/gorenx/goren/llm/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

type command struct{ payload any }
type response struct {
	payload any
	err     error
}
type requester struct {
	target  *goakt.GrainIdentity
	replies chan response
}

func (*requester) PreStart(*goakt.Context) error { return nil }
func (*requester) PostStop(*goakt.Context) error { return nil }
func (driver *requester) Receive(rc *goakt.ReceiveContext) {
	if input, ok := rc.Message().(command); ok {
		replies := driver.replies
		rc.RequestGrain(driver.target, input.payload).Then(func(payload any, failure error) {
			replies <- response{
				payload: payload,
				err:     failure,
			}
		})
	}
}

func registryDriver(t *testing.T, system goakt.ActorSystem) (*goakt.PID, <-chan response) {
	t.Helper()
	target, err := goakt.GrainOf[*llmactor.Grain](context.Background(), system, "registry", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	replies := make(chan response, 1)
	driver, err := system.Spawn(context.Background(), "requester", &requester{
		target:  target,
		replies: replies,
	}, goakt.WithReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		t.Fatal(err)
	}
	return driver, replies
}

func request[T any](t *testing.T, driver *goakt.PID, replies <-chan response, payload any) T {
	t.Helper()
	if err := goakt.Tell(context.Background(), driver, command{
		payload: payload,
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-replies:
		if result.err != nil {
			t.Fatal(result.err)
		}
		typed, ok := result.payload.(T)
		if !ok {
			t.Fatalf("unexpected response %T", result.payload)
		}
		return typed
	case <-time.After(3 * time.Second):
		t.Fatal("registry request timed out")
	}
	var zero T
	return zero
}

type observer struct {
	ready  chan struct{}
	events chan struct{}
}

func (*observer) PreStart(*goakt.Context) error { return nil }
func (*observer) PostStop(*goakt.Context) error { return nil }
func (sink *observer) Receive(rc *goakt.ReceiveContext) {
	switch rc.Message().(type) {
	case *goakt.PostStart:
		rc.Tell(rc.ActorSystem().TopicActor(), goakt.NewSubscribe(llm.AdaptersUpdatedEventName))
	case *goakt.SubscribeAck:
		close(sink.ready)
	case llm.AdaptersUpdated:
		sink.events <- struct{}{}
	}
}

func TestRegistryPublishesCommittedMutation(t *testing.T) {
	system := startSystem(t, goakt.WithPubSub())
	driver, replies := registryDriver(t, system)
	ready := make(chan struct{})
	events := make(chan struct{}, 4)
	if _, err := system.Spawn(context.Background(), "observer", &observer{
		ready:  ready,
		events: events,
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("subscription timed out")
	}
	registered := request[llmactor.Registered](t, driver, replies, llmactor.RegisterAdapter{
		Providers: []string{"p"},
		Adapter:   &providerFixture{},
	})
	if registered.Err != nil || registered.ID == "" {
		t.Fatalf("registration: %#v", registered)
	}
	select {
	case <-events:
	case <-time.After(time.Second):
		t.Fatal("no committed update")
	}
	selected := request[llmactor.Selected](t, driver, replies, llmactor.Select{
		Provider: "p",
	})
	if selected.Err != nil || selected.Binding == nil {
		t.Fatalf("published uncommitted state: %#v", selected)
	}
	removed := request[llmactor.Completion](t, driver, replies, llmactor.RemoveAdapter{
		ID: registered.ID,
	})
	if removed.Err != nil || !removed.Changed {
		t.Fatalf("remove: %#v", removed)
	}
	select {
	case <-events:
	case <-time.After(time.Second):
		t.Fatal("no removal update")
	}
	duplicate := request[llmactor.Completion](t, driver, replies, llmactor.RemoveAdapter{
		ID: registered.ID,
	})
	if duplicate.Err != nil || duplicate.Changed {
		t.Fatalf("duplicate removal: %#v", duplicate)
	}
}

func TestPublicationFailureReturnsCommittedRegistration(t *testing.T) {
	system := startSystem(t)
	driver, replies := registryDriver(t, system)
	registered := request[llmactor.Registered](t, driver, replies, llmactor.RegisterAdapter{
		Providers: []string{"p"},
		Adapter:   &providerFixture{},
	})
	if registered.ID == "" || registered.Err == nil {
		t.Fatalf("missing committed identity or PubSub failure: %#v", registered)
	}
	selected := request[llmactor.Selected](t, driver, replies, llmactor.Select{
		Provider: "p",
	})
	if selected.Err != nil || selected.Binding == nil {
		t.Fatal("notification failure hid committed state")
	}
}
