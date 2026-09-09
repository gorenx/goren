package toolruntime_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	toolregistry "github.com/gorenx/goren/tools/registry"
	toolruntime "github.com/gorenx/goren/tools/runtime"
)

func (port *catalogPort) ReadConfiguration(_ context.Context, identifier toolregistry.UpdateID) (toolregistry.Configuration, error) {
	configured, found := port.business.ReadConfiguration(identifier)
	if !found {
		return configured, errInjected
	}
	return configured, nil
}

func TestRuntimeCloseResolvesUpdateBeforeRemovingTools(t *testing.T) {
	for _, published := range []bool{false, true} {
		name := "unpublished"
		if published {
			name = "published"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			directory := &catalogPort{}
			if err := directory.business.InitializeDefaults([]toolregistry.Tool{{
				Name:     "echo",
				Provider: unusedProvider{},
				Visible:  true,
			}}); err != nil {
				t.Fatal(err)
			}
			if _, err := directory.business.Configure("initial", "agent", nil); err != nil {
				t.Fatal(err)
			}
			instances := &instancePort{
				directory:      directory,
				fail:           "prepare:echo",
				cleanupFailure: "abort:echo",
			}
			if published {
				instances.fail = "commit:echo"
				instances.cleanupFailure = ""
			}
			business, err := toolruntime.NewRuntime("agent")
			if err != nil {
				t.Fatal(err)
			}
			if complete, err := business.Apply(ctx, "initial", directory, instances); complete || err == nil {
				t.Fatal("failed update did not retain cleanup")
			}
			if err := business.Close(ctx, directory, instances); err == nil {
				t.Fatal("close discarded failed update cleanup")
			}
			if complete, err := business.Apply(ctx, "initial", directory, instances); complete || err == nil {
				t.Fatal("closing runtime accepted a new update attempt")
			}
			instances.fail = ""
			instances.cleanupFailure = "remove:echo"
			if err := business.Close(ctx, directory, instances); err == nil {
				t.Fatal("close discarded failed instance removal")
			}
			instances.cleanupFailure = ""
			if err := business.Close(ctx, directory, instances); err != nil {
				t.Fatal(err)
			}
			before := slices.Clone(instances.operations)
			if err := business.Close(ctx, directory, instances); err != nil || !slices.Equal(before, instances.operations) {
				t.Fatal("completed close repeated resource operations")
			}
			if _, err := business.Snapshot(); err == nil {
				t.Fatal("closed runtime exposed an executable snapshot")
			}
			if _, found := directory.business.Lookup("initial"); found {
				t.Fatal("close retained unsettled update")
			}
			if published && slices.Contains(instances.operations, "abort:echo") {
				t.Fatal("close rolled back a published tool")
			}
			if !published && slices.Contains(instances.operations, "commit:echo") {
				t.Fatal("close committed an unpublished failed update")
			}
		})
	}
}

func TestNewRuntimeRebuildsInstancesAgainstRetainedCatalog(t *testing.T) {
	ctx := context.Background()
	directory := &catalogPort{}
	err := directory.business.InitializeDefaults([]toolregistry.Tool{{
		Name:     "echo",
		Provider: unusedProvider{},
		Visible:  true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = directory.business.Configure("first", "agent", nil)
	if err != nil {
		t.Fatal(err)
	}
	previous, err := toolruntime.NewRuntime("agent")
	if err != nil {
		t.Fatal(err)
	}
	original := &instancePort{directory: directory}
	complete, err := previous.Apply(ctx, "first", directory, original)
	if err != nil || !complete {
		t.Fatal("initial update failed", err)
	}
	err = previous.Close(ctx, directory, original)
	if err != nil {
		t.Fatal(err)
	}
	retained, found := directory.business.Read("agent")
	if !found || len(retained.Schemas) != 1 || retained.Schemas[0].Version != 1 {
		t.Fatal("close erased catalog revision")
	}

	replacement, err := toolruntime.NewRuntime("agent")
	if err != nil {
		t.Fatal(err)
	}
	fresh := &instancePort{
		directory: directory,
		fail:      "prepare:echo",
	}
	_, err = directory.business.Configure("failed-rebuild", "agent", nil)
	if err != nil {
		t.Fatal(err)
	}
	complete, err = replacement.Apply(ctx, "failed-rebuild", directory, fresh)
	if !complete || err == nil {
		t.Fatal("first update did not complete rollback against retained version", err)
	}
	_, err = replacement.Snapshot()
	if err == nil {
		t.Fatal("old catalog was exposed as new runtime ownership")
	}
	fresh.fail = ""
	fresh.operations = nil
	_, err = directory.business.Configure("rebuild", "agent", nil)
	if err != nil {
		t.Fatal(err)
	}
	complete, err = replacement.Apply(ctx, "rebuild", directory, fresh)
	if err != nil || !complete {
		t.Fatal("new lifetime could not rebuild tools", err)
	}
	snapshot, err := replacement.Snapshot()
	if err != nil || !slices.Equal(snapshot.Names, []string{"echo"}) {
		t.Fatal("wrong replacement snapshot", snapshot, err)
	}
	if !slices.Contains(fresh.operations, "prepare:echo") || !slices.Contains(fresh.operations, "commit:echo") {
		t.Fatal("new lifetime reused old instances", fresh.operations)
	}
	err = replacement.Close(ctx, directory, fresh)
	if err != nil {
		t.Fatal(err)
	}
}

func TestInitializationRetainsUnconfirmedRegistrationUntilClose(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		name := "request-not-accepted"
		if accepted {
			name = "accepted-reply-lost"
		}
		t.Run(name, func(t *testing.T) {
			directory := &catalogPort{
				registrationLost:     accepted,
				registrationRejected: !accepted,
			}
			err := directory.business.InitializeDefaults([]toolregistry.Tool{{
				Name:     "echo",
				Provider: unusedProvider{},
				Visible:  true,
			}})
			if err != nil {
				t.Fatal(err)
			}
			owner, err := toolruntime.NewRuntime("agent")
			if err != nil {
				t.Fatal(err)
			}
			instances := &instancePort{directory: directory}
			err = owner.Initialize(context.Background(), "initial", nil, directory, instances)
			if !errors.Is(err, errInjected) {
				t.Fatal("initialization failure lost", err)
			}
			if len(instances.operations) != 0 {
				t.Fatal("unconfirmed registration constructed tools", instances.operations)
			}
			_, found := directory.business.Lookup("initial")
			if found != accepted {
				t.Fatal("fixture did not produce expected registration outcome")
			}
			_, err = owner.Snapshot()
			if err == nil {
				t.Fatal("failed initialization exposed a snapshot")
			}
			directory.lookupFailure = true
			err = owner.Close(context.Background(), directory, instances)
			if !errors.Is(err, errInjected) {
				t.Fatal("unknown registration cleanup was reported as complete", err)
			}
			directory.lookupFailure = false
			if accepted {
				directory.abortFailure = true
				err = owner.Close(context.Background(), directory, instances)
				if !errors.Is(err, errInjected) {
					t.Fatal("failed abort lost cleanup responsibility", err)
				}
				directory.abortFailure = false
			}
			err = owner.Close(context.Background(), directory, instances)
			if err != nil {
				t.Fatal(err)
			}
			_, found = directory.business.Lookup("initial")
			if found {
				t.Fatal("closed initialization left a retained configuration")
			}
			err = owner.Close(context.Background(), directory, instances)
			if err != nil || len(instances.operations) != 0 {
				t.Fatal("repeated close constructed or removed nonexistent instances", err)
			}
		})
	}
}
