package toolactor_test

import (
	"context"
	"testing"
	"time"

	toolregistry "github.com/gorenx/goren/tools/registry"
	toolactor "github.com/gorenx/goren/tools/registry/actor"
	"github.com/gorenx/goren/tools/tool"
	goakt "github.com/tochemey/goakt/v4/actor"
)

type unopened struct {
	label string
}

func (*unopened) Open(context.Context) (tool.Implementation, error) {
	panic("configuration registration must not open a tool")
}

type configurationObserver struct {
	ready  chan struct{}
	events chan toolregistry.ConfigurationRegistered
}

func (*configurationObserver) PreStart(*goakt.Context) error { return nil }
func (*configurationObserver) PostStop(*goakt.Context) error { return nil }
func (probe *configurationObserver) Receive(rc *goakt.ReceiveContext) {
	switch incoming := rc.Message().(type) {
	case *goakt.PostStart:
		rc.Tell(rc.ActorSystem().TopicActor(), goakt.NewSubscribe(toolregistry.ConfigurationTopic))
	case *goakt.SubscribeAck:
		probe.ready <- struct{}{}
	case toolregistry.ConfigurationRegistered:
		probe.events <- incoming
	}
}

func TestConfigurationEventIdentifiesExactRetainedOverrides(t *testing.T) {
	system, target := startRegistry(t, true)
	probe := &configurationObserver{
		ready:  make(chan struct{}, 1),
		events: make(chan toolregistry.ConfigurationRegistered, 1),
	}
	if _, err := system.Spawn(context.Background(), "configuration-observer", probe); err != nil {
		t.Fatal(err)
	}
	select {
	case <-probe.ready:
	case <-time.After(time.Second):
		t.Fatal("subscription timed out")
	}
	if reply := ask[toolactor.Configuration](t, system, target, toolactor.RegisterConfiguration{
		ID:      "update",
		AgentID: "agent",
	}); reply.Err == nil || reply.Found {
		t.Fatal("uninitialized defaults became a configuration")
	}
	if reply := ask[toolactor.Completion](t, system, target, toolactor.InitializeDefaults{
		Tools: []toolregistry.Tool{{
			Name: "echo",
			Provider: &unopened{
				label: "global",
			},
			Visible: true,
		}},
	}); reply.Err != nil {
		t.Fatal(reply.Err)
	}
	local := &unopened{
		label: "agent",
	}
	reply := ask[toolactor.Configuration](t, system, target, toolactor.RegisterConfiguration{
		ID:      "update",
		AgentID: "agent",
		Overrides: []toolregistry.Tool{{
			Name:     "echo",
			Provider: local,
			Visible:  false,
		}},
	})
	if reply.Err != nil || !reply.Found || len(reply.Value.Tools) != 1 || reply.Value.Tools[0].Provider != local || reply.Value.Tools[0].Visible {
		t.Fatalf("configuration = %#v", reply)
	}
	reply.Value.Tools[0].Name = "mutated"
	select {
	case event := <-probe.events:
		if event.ID != "update" || event.AgentID != "agent" {
			t.Fatalf("configuration event = %#v", event)
		}
		captured := ask[toolactor.Configuration](t, system, target, toolactor.ReadConfiguration{
			ID: event.ID,
		})
		if captured.Err != nil || !captured.Found || captured.Value.Tools[0].Name != "echo" || captured.Value.Tools[0].Provider != local {
			t.Fatalf("retained configuration = %#v", captured)
		}
	case <-time.After(time.Second):
		t.Fatal("configuration event was not delivered")
	}
	if baseline := ask[toolactor.Catalog](t, system, target, toolactor.ReadDefinitions{
		AgentID: "agent",
	}); baseline.Found {
		t.Fatal("configuration event exposed an unprepared prompt baseline")
	}
}

func TestConfigurationRemainsQueryableWhenPubSubIsUnavailable(t *testing.T) {
	system, target := startRegistry(t, false)
	if reply := ask[toolactor.Completion](t, system, target, toolactor.InitializeDefaults{}); reply.Err != nil {
		t.Fatal(reply.Err)
	}
	input := toolactor.RegisterConfiguration{
		ID:      "empty",
		AgentID: "agent",
	}
	registered := ask[toolactor.Configuration](t, system, target, input)
	if registered.Err == nil || !registered.Found || registered.Value.Update.ID != "empty" {
		t.Fatalf("notification failure hid registration: %#v", registered)
	}
	if duplicate := ask[toolactor.Configuration](t, system, target, input); duplicate.Err == nil {
		t.Fatal("duplicate configuration was registered again")
	}
	captured := ask[toolactor.Configuration](t, system, target, toolactor.ReadConfiguration{
		ID: "empty",
	})
	if captured.Err != nil || !captured.Found || captured.Value.Update.AgentID != "agent" || len(captured.Value.Tools) != 0 {
		t.Fatalf("configuration recovery = %#v", captured)
	}
}
