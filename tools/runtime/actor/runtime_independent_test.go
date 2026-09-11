package runtimeactor_test

import (
	"testing"
	"time"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/toolaskuser"
	"github.com/gorenx/goren/tools"
	toolregistry "github.com/gorenx/goren/tools/registry"
	catalogactor "github.com/gorenx/goren/tools/registry/actor"
	runtimeactor "github.com/gorenx/goren/tools/runtime/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

func initializeConfiguredRuntime(t *testing.T, system goakt.ActorSystem, registry *goakt.GrainIdentity, identifier session.SessionID, configuration *toolregistry.Catalog) (*goakt.GrainIdentity, runtimeactor.CloseRuntime) {
	t.Helper()
	authority, err := goakt.GrainOf[*runtimeAuthority](t.Context(), system, string(identifier)+".authority", goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	target, err := goakt.GrainOf[*runtimeactor.Runtime](t.Context(), system, runtimeactor.RuntimeName(string(identifier)), goakt.WithLongLivedGrain(), goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		t.Fatal(err)
	}
	initialized := ask[runtimeactor.Completion](t, system, target, runtimeactor.InitializeRuntime{
		AgentID:    identifier,
		CreationID: "creation",
		Authority:  authority,
		Registry:   registry,
		Config:     configuration,
		Timeout:    time.Second,
	})
	if initialized.Err != nil {
		t.Fatal(initialized.Err)
	}
	_, err = system.Spawn(t.Context(), string(identifier)+".watcher", &runtimeactor.Watcher{
		AgentID:    string(identifier),
		CreationID: "creation",
		Target:     target,
	}, goakt.WithReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		t.Fatal(err)
	}
	return target, runtimeactor.CloseRuntime{
		CreationID: "creation",
		InstanceID: ask[string](t, system, authority, readClaim{}),
	}
}

func TestRuntimeIndependentSessionsBindSameNameToOwnProviderAndRejectFallback(t *testing.T) {
	globalFirst := &identifiedSource{
		id: "global-first",
	}
	globalNext := &identifiedSource{
		id: "global-next",
	}
	local := &identifiedSource{
		id: "child-local",
	}
	globalSource := &dynamicSource{}
	globalSource.current.Store(globalFirst)
	localSource := &dynamicSource{}
	localSource.current.Store(local)
	system, registry, _ := startCatalog(t, true, nil, globalSource, localSource)
	definition, err := toolaskuser.NewDefinition(questions{})
	if err != nil {
		t.Fatal(err)
	}
	selectionFor := func(identifier tools.ProviderID) toolregistry.Catalog {
		return toolregistry.Catalog{
			Schemas: []llm.ToolSchema{
				{
					Name:        definition.Name,
					Description: definition.Description,
					Parameters:  definition.Parameters,
				},
			},
			ProviderIDs: map[string]tools.ProviderID{
				definition.Name: identifier,
			},
		}
	}
	publish := func(identifier tools.ProviderID) {
		t.Helper()
		result := ask[catalogactor.Completion](t, system, registry, catalogactor.RegisterSource{
			Name:    "global",
			Catalog: selectionFor(identifier),
		})
		if result.Err != nil {
			t.Fatal(result.Err)
		}
	}
	publish(globalFirst.id)
	parent, parentClose := initializeConfiguredRuntime(t, system, registry, "parent", nil)
	selection := selectionFor(local.id)
	selection.InheritGlobal = false
	child, childClose := initializeConfiguredRuntime(t, system, registry, "child", &selection)
	empty, emptyClose := initializeConfiguredRuntime(t, system, registry, "empty", &toolregistry.Catalog{
		InheritGlobal: false,
	})
	if globalFirst.opens.Load() != 1 || local.opens.Load() != 1 {
		t.Fatalf("initialization selected wrong providers: global=%d local=%d", globalFirst.opens.Load(), local.opens.Load())
	}
	read := func(target *goakt.GrainIdentity) runtimeactor.RuntimeView {
		t.Helper()
		result := ask[runtimeactor.RuntimeView](t, system, target, runtimeactor.ReadRuntime{
			CreationID: "creation",
		})
		if result.Err != nil {
			t.Fatal(result.Err)
		}
		return result
	}
	if len(read(parent).Snapshot.Names) != 1 ||
		len(read(child).Snapshot.Names) != 1 ||
		len(read(empty).Snapshot.Names) != 0 {
		t.Fatal("independent selection was not applied through initialization")
	}
	// Publish the replacement and wait for the ordinary global subscriber to
	// adopt it. Child runtimes receive the same notice but retain their selection.
	globalSource.current.Store(globalNext)
	publish(globalNext.id)
	refresh := func(target *goakt.GrainIdentity, identifier string) runtimeactor.Definitions {
		return ask[runtimeactor.Definitions](t, system, target, runtimeactor.RefreshConfiguration{
			CreationID: "creation",
			ID:         identifier,
		})
	}
	updated := awaitProviderRefresh(t, func() runtimeactor.Definitions {
		return refresh(parent, "global-update")
	}, func(result runtimeactor.Definitions) bool {
		return result.Err != nil || globalNext.opens.Load() != 0
	})
	if updated.Err != nil || globalFirst.closes.Load() != 1 {
		t.Fatalf("parent did not switch: %+v closes=%d", updated, globalFirst.closes.Load())
	}
	for _, target := range []*goakt.GrainIdentity{child, empty} {
		if result := refresh(target, "observe-global-update"); result.Err != nil {
			t.Fatal(result.Err)
		}
	}
	if local.opens.Load() != 1 || local.closes.Load() != 0 || len(read(empty).Snapshot.Names) != 0 {
		t.Fatal("global publication rebuilt independent work")
	}
	// Force a real child replacement after withdrawing its exact constructor.
	// A same-name global constructor must never be used as a fallback.
	localSource.current.Store(nil)
	selection.Schemas[0].Description = "changed local definition"
	configured := ask[catalogactor.Completion](t, system, registry, catalogactor.ConfigureSession{
		SessionID: "child",
		Catalog:   selection,
	})
	if configured.Err != nil {
		t.Fatal(configured.Err)
	}
	failed := awaitProviderRefresh(t, func() runtimeactor.Definitions {
		return refresh(child, "local-replacement")
	}, func(result runtimeactor.Definitions) bool {
		return result.Err != nil
	})
	if failed.Err == nil || globalNext.opens.Load() != 1 || local.opens.Load() != 1 || local.closes.Load() != 0 ||
		read(child).Snapshot.Schemas[0].Description != definition.Description {
		t.Fatal("failed exact binding used fallback or changed the active schema")
	}
	localSource.current.Store(local)
	if result := refresh(child, "retry-local-replacement"); result.Err != nil {
		t.Fatal(result.Err)
	}
	if local.opens.Load() != 2 || local.closes.Load() != 1 {
		t.Fatal("child did not replace its own work")
	}
	for _, entry := range []struct {
		target *goakt.GrainIdentity
		close  runtimeactor.CloseRuntime
	}{
		{
			target: empty,
			close:  emptyClose,
		},
		{
			target: child,
			close:  childClose,
		},
		{
			target: parent,
			close:  parentClose,
		},
	} {
		if result := ask[runtimeactor.Completion](t, system, entry.target, entry.close); result.Err != nil {
			t.Fatal(result.Err)
		}
	}
	if globalNext.closes.Load() != 1 || local.closes.Load() != 2 {
		t.Fatalf("resources were not released independently: global=%d child=%d", globalNext.closes.Load(), local.closes.Load())
	}
}
