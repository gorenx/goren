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
	// New registrations do not change any running lifetime.
	globalSource.current.Store(globalNext)
	publish(globalNext.id)
	localSource.current.Store(nil)
	for _, target := range []*goakt.GrainIdentity{parent, child, empty} {
		result := ask[runtimeactor.Definitions](t, system, target, runtimeactor.RefreshConfiguration{
			CreationID: "creation",
			ID:         "discovery",
		})
		if result.Err != nil {
			t.Fatal(result.Err)
		}
	}
	if globalFirst.closes.Load() != 0 || globalNext.opens.Load() != 0 || local.closes.Load() != 0 || len(read(empty).Snapshot.Names) != 0 {
		t.Fatal("registration changed an active lifetime")
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
	if globalFirst.closes.Load() != 1 || local.closes.Load() != 1 {
		t.Fatalf("resources were not released independently: global=%d child=%d", globalFirst.closes.Load(), local.closes.Load())
	}
}
