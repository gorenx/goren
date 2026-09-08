package registry_test

import (
	"context"
	"testing"
	"time"

	agentactor "github.com/gorenx/goren/agent/actor"
	agentregistry "github.com/gorenx/goren/agent/registry"
	sessionapi "github.com/gorenx/goren/apiproxy/session"
	sessionadapter "github.com/gorenx/goren/apiproxy/session/actor"
	"github.com/gorenx/goren/session"
	projectionactor "github.com/gorenx/goren/session/projection/actor"
	sessionregistry "github.com/gorenx/goren/session/registry"
	"github.com/gorenx/goren/session/title"
	titleregistry "github.com/gorenx/goren/session/title/registry"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

func TestDomainClientsUseIndependentTitleAndAgentOwners(t *testing.T) {
	system, authority, setup := creationFixture(t, goakt.WithPubSub())
	ctx := context.Background()
	titles, err := goakt.GrainOf[*titleregistry.Grain](ctx, system, titleregistry.Name,
		goakt.WithLongLivedGrain(), goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		t.Fatal(err)
	}
	configured := askLifecycle[titleregistry.Configured](t, system, titles, titleregistry.Configure{
		Config: title.Config{
			MaxTitleBytes:    80,
			FallbackMaxBytes: 40,
			FallbackMaxWords: 3,
		},
		Timeout: time.Second,
	})
	if configured.Err != nil {
		t.Fatal(configured.Err)
	}
	setup.Components = []*goakt.GrainIdentity{titles}
	directory, err := sessionadapter.NewDirectory(system, authority, setup, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	naming, err := sessionadapter.NewTitles(system, authority, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	cwd := "/project"
	subject, err := directory.Create(ctx, sessionapi.Opening{
		ID: "conversation",
		Options: session.CreateOptions{
			Metadata: session.Metadata{
				CWD: &cwd,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := subject.Pending(ctx); err != nil {
		t.Fatal(err)
	}
	accepted, err := naming.Rename(ctx, subject.ID(), "saved title")
	if err != nil || accepted.Text != "saved title" {
		t.Fatalf("rename: %+v %v", accepted, err)
	}
	projected, err := goakt.GrainOf[*projectionactor.Grain](ctx, system, "projection", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	registration := askLifecycle[projectionactor.Registered](t, system, projected, projectionactor.Register{
		Unit: title.Unit{},
	})
	if registration.Err != nil {
		t.Fatal(registration.Err)
	}
	histories, err := sessionadapter.NewLogs(system, setup.SessionRegistry, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	views, err := sessionadapter.NewProjections(system, projected, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := sessionapi.NewReader(histories, views, directory)
	if err != nil {
		t.Fatal(err)
	}
	original, err := reader.Read(ctx, subject.ID())
	if err != nil || original == nil {
		t.Fatalf("read view: %+v %v", original, err)
	}
	if string(original.Projections.Values[title.ProjectionKey]) != `"saved title"` {
		t.Fatalf("projection: %+v", original.Projections)
	}
	if missing, err := reader.Read(ctx, "absent"); err != nil || missing != nil {
		t.Fatalf("missing read: %+v %v", missing, err)
	}
	ready := askLifecycle[agentregistry.Ready](t, system, authority, agentregistry.Resolve{
		AgentID:   subject.ID(),
		WaitReady: true,
	})
	if ready.Err != nil {
		t.Fatal(ready.Err)
	}
	closed := askLifecycle[agentactor.Closed](t, system, authority, agentregistry.CloseAgent{
		Creation: ready.Creation,
	})
	if closed.Err != nil {
		t.Fatal(closed.Err)
	}
	if _, err := subject.Pending(ctx); err == nil {
		t.Fatal("closed client read succeeded")
	}
	replacement, err := directory.Create(ctx, sessionapi.Opening{
		ID:      "conversation",
		Restore: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := subject.Pending(ctx); err == nil {
		t.Fatal("old client read replacement")
	}
	if _, err := replacement.Pending(ctx); err != nil {
		t.Fatal(err)
	}
	accepted, err = naming.Rename(ctx, replacement.ID(), "new title")
	if err != nil || accepted.Text != "new title" {
		t.Fatalf("replacement title: %+v %v", accepted, err)
	}
	replacementView, err := reader.Read(ctx, replacement.ID())
	if err != nil || replacementView == nil {
		t.Fatalf("replacement read: %+v %v", replacementView, err)
	}
	if string(replacementView.Projections.Values[title.ProjectionKey]) != `"new title"` {
		t.Fatalf("replacement projection: %+v", replacementView.Projections)
	}
	historicalView, err := views.Read(ctx, original.Events)
	if err != nil || string(historicalView.Values[title.ProjectionKey]) != `"saved title"` {
		t.Fatalf("captured prefix: %+v %v", historicalView, err)
	}
	latest := askLifecycle[agentregistry.Ready](t, system, authority, agentregistry.Resolve{
		AgentID:   replacement.ID(),
		WaitReady: true,
	})
	stale := askLifecycle[agentactor.ComponentStopped](t, system, titles, agentactor.StopComponent{
		AgentID:    subject.ID(),
		CreationID: ready.Creation.CreationID,
	})
	if stale.Err == nil {
		t.Fatal("old closure accepted after replacement")
	}
	final := askLifecycle[agentactor.Closed](t, system, authority, agentregistry.CloseAgent{
		Creation: latest.Creation,
	})
	if final.Err != nil {
		t.Fatal(final.Err)
	}
	// The component closes before Session flush; title commits remain durable.
	inspection, err := setup.Storage.Load(ctx, session.SessionID("conversation"))
	if err != nil || inspection == nil || inspection.Header.ID != "conversation" {
		t.Fatalf("durability: %+v %v", inspection, err)
	}
}

func TestTitleComponentRecoversAfterSessionBindingFailure(t *testing.T) {
	system, authority, setup := creationFixture(t, goakt.WithPubSub())
	ctx := context.Background()
	titles, err := goakt.GrainOf[*titleregistry.Grain](ctx, system, titleregistry.Name,
		goakt.WithLongLivedGrain(), goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		t.Fatal(err)
	}
	configured := askLifecycle[titleregistry.Configured](t, system, titles, titleregistry.Configure{
		Config: title.Config{
			MaxTitleBytes:    80,
			FallbackMaxBytes: 40,
			FallbackMaxWords: 3,
		},
		Timeout: time.Second,
	})
	if configured.Err != nil {
		t.Fatal(configured.Err)
	}
	setup.Creation = agentregistry.Creation{
		AgentID:    "title-recovery",
		CreationID: "current",
	}
	ready := askLifecycle[agentregistry.Ready](t, system, authority, setup)
	if ready.Err != nil {
		t.Fatal(ready.Err)
	}
	route := askLifecycle[sessionregistry.Endpoint](t, system, setup.SessionRegistry, sessionregistry.ResolveSession{
		SessionID:  setup.Creation.AgentID,
		CreationID: setup.Creation.CreationID,
	})
	if route.Err != nil {
		t.Fatal(route.Err)
	}
	// A Title Grain is allocated before Session rejects this stale binding.
	failed := askLifecycle[agentactor.ComponentStarted](t, system, titles, agentactor.StartComponent{
		AgentID:    setup.Creation.AgentID,
		CreationID: "stale",
		Session:    route.Target,
	})
	if failed.Err == nil || failed.Runtime != nil {
		t.Fatalf("stale startup: %+v", failed)
	}
	for range 2 {
		closed := askLifecycle[agentactor.ComponentStopped](t, system, titles, agentactor.StopComponent{
			AgentID:    setup.Creation.AgentID,
			CreationID: "stale",
		})
		if closed.Err != nil {
			t.Fatal(closed.Err)
		}
	}
	for _, identity := range []string{"stale", "current"} {
		started := askLifecycle[agentactor.ComponentStarted](t, system, titles, agentactor.StartComponent{
			AgentID:    setup.Creation.AgentID,
			CreationID: identity,
			Session:    route.Target,
		})
		if identity == "stale" {
			if started.Err == nil {
				t.Fatal("retired creation restarted")
			}
		} else if started.Err != nil || started.Runtime == nil {
			t.Fatalf("current startup after cleanup: %+v", started)
		}
	}
	closed := askLifecycle[agentactor.ComponentStopped](t, system, titles, agentactor.StopComponent{
		AgentID:    setup.Creation.AgentID,
		CreationID: "current",
	})
	if closed.Err != nil {
		t.Fatal(closed.Err)
	}
	final := askLifecycle[agentactor.Closed](t, system, authority, agentregistry.CloseAgent{
		Creation: ready.Creation,
	})
	if final.Err != nil {
		t.Fatal(final.Err)
	}
}
