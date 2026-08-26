package childdirectory

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/gorenx/goren/session"
	sessionpersistence "github.com/gorenx/goren/session/persistence"
	sessionprojection "github.com/gorenx/goren/session/projection"
	"github.com/gorenx/goren/subagent"
)

type identityCache struct {
	snapshots map[session.SessionID]sessionprojection.Snapshot
}

func (cache identityCache) CachedSnapshot(
	metadata session.Header,
) (*sessionprojection.Snapshot, error) {
	projectionSnapshot, found := cache.snapshots[metadata.ID]
	if !found {
		return nil, nil
	}
	return &projectionSnapshot, nil
}

type countingInspection struct {
	*persistenceView
	mutex sync.Mutex
	calls int
}

func (source *countingInspection) Inspect(
	requestContext context.Context,
	identifier session.SessionID,
) (sessionpersistence.Inspection, error) {
	source.mutex.Lock()
	source.calls++
	source.mutex.Unlock()
	return source.persistenceView.Inspect(requestContext, identifier)
}

func (source *countingInspection) count() int {
	source.mutex.Lock()
	defer source.mutex.Unlock()
	return source.calls
}

func cachedIdentitySnapshot(
	t *testing.T,
	registry *sessionprojection.DriveRegistry,
	conversation session.Context,
) sessionprojection.Snapshot {
	t.Helper()
	restored, err := registry.Restore(
		nil,
		conversation.Events(),
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	return restored.Snapshot
}

func TestColdChildUsesCachedIdentityWithoutInspect(t *testing.T) {
	rootID := session.SessionID("cache-root")
	child := newDirectorySession(
		t,
		"cached-child",
		rootID,
		1,
		subagent.ContinuableDescriptor{
			Provider: "fork",
			Label:    "cached",
		},
	)
	registry := projectionRegistry(t)
	durability := &countingInspection{
		persistenceView: &persistenceView{
			headers: []session.Header{child.Header()},
			failures: map[session.SessionID]error{
				child.ID(): errors.New("Inspect must not run on cache hit"),
			},
		},
	}
	directory := New()
	if err := directory.Enable(
		sessionList{},
		durability,
		registry,
		identityCache{
			snapshots: map[session.SessionID]sessionprojection.Snapshot{
				child.ID(): cachedIdentitySnapshot(t, registry, child),
			},
		},
	); err != nil {
		t.Fatal(err)
	}
	entries, err := directory.ListChildren(context.Background(), rootID)
	if err != nil {
		t.Fatal(err)
	}
	wanted := []subagent.ChildEntry{
		subagent.ContinuableChildEntry{
			ID:          child.ID(),
			Label:       "cached",
			Activity:    subagent.ActivityInactive,
			HasChildren: false,
		},
	}
	if !reflect.DeepEqual(entries, wanted) || durability.count() != 0 {
		t.Fatalf("entries = %#v, Inspect calls = %d", entries, durability.count())
	}
}

func TestColdChildRejectsCachedAncestorIdentityBeforeSeed(t *testing.T) {
	rootID := session.SessionID("seed-root")
	child := newDirectorySession(
		t,
		"seed-child",
		rootID,
		1,
		subagent.ContinuableDescriptor{
			Provider: "fork",
			Label:    "child",
		},
	)
	registry := projectionRegistry(t)
	header := child.Header()
	header.SeedLength = int64Pointer(1)
	durability := &countingInspection{
		persistenceView: &persistenceView{
			headers: []session.Header{header},
			inspections: map[session.SessionID]sessionpersistence.Inspection{
				child.ID(): {
					Header: header,
					Events: child.Events(),
				},
			},
		},
	}
	directory := New()
	if err := directory.Enable(
		sessionList{},
		durability,
		registry,
		identityCache{
			snapshots: map[session.SessionID]sessionprojection.Snapshot{
				child.ID(): cachedIdentitySnapshot(t, registry, child),
			},
		},
	); err != nil {
		t.Fatal(err)
	}
	entries, err := directory.ListChildren(context.Background(), rootID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || durability.count() != 1 {
		t.Fatalf("entries = %#v, Inspect calls = %d", entries, durability.count())
	}
}
