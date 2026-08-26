package childdirectory

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/gorenx/goren/session"
	sessionpersistence "github.com/gorenx/goren/session/persistence"
	sessionprojection "github.com/gorenx/goren/session/projection"
	"github.com/gorenx/goren/subagent"
	subagentprojection "github.com/gorenx/goren/subagent/internal/projection"
)

type sessionList struct {
	conversations []session.Context
}

func (source sessionList) List() []session.Context {
	return append([]session.Context(nil), source.conversations...)
}

type persistenceView struct {
	headers     []session.Header
	inspections map[session.SessionID]sessionpersistence.Inspection
	failures    map[session.SessionID]error
	listErr     error
}

func (source *persistenceView) List(context.Context) ([]session.Header, error) {
	if source.listErr != nil {
		return nil, source.listErr
	}
	return append([]session.Header(nil), source.headers...), nil
}

func (source *persistenceView) Inspect(
	_ context.Context,
	identifier session.SessionID,
) (sessionpersistence.Inspection, error) {
	if failure := source.failures[identifier]; failure != nil {
		return sessionpersistence.Inspection{}, failure
	}
	inspection, found := source.inspections[identifier]
	if !found {
		return sessionpersistence.Inspection{}, errors.New("missing inspection")
	}
	return sessionpersistence.Inspection{
		Header: inspection.Header,
		Events: append([]session.Event(nil), inspection.Events...),
	}, nil
}

func TestListChildrenUsesLivePreferredProjectionBackedCorpus(t *testing.T) {
	t.Parallel()
	rootID := session.SessionID("root")
	creationWindow := newDirectorySession(t, "creation", rootID, 5, nil)
	oneShotLabel := "search"
	coldOneShot := newDirectorySession(
		t,
		"cold-one-shot",
		rootID,
		10,
		subagent.OneShotDescriptor{
			Provider: "spawn",
			Label:    &oneShotLabel,
		},
	)
	corrupt := newDirectorySession(t, "corrupt", rootID, 15, nil)
	liveContinuable := newDirectorySession(
		t,
		"live-continuable",
		rootID,
		20,
		subagent.ContinuableDescriptor{
			Provider: "fork",
			Label:    "review",
		},
	)
	unavailable := newDirectorySession(
		t,
		"unavailable",
		rootID,
		25,
		subagent.OneShotDescriptor{
			Provider: "spawn",
		},
	)
	grandchild := newDirectorySession(
		t,
		"grandchild",
		coldOneShot.ID(),
		30,
		subagent.ContinuableDescriptor{
			Provider: "fork",
			Label:    "nested",
		},
	)

	staleLiveHeader := liveContinuable.Header()
	staleLiveHeader.CreatedAt = 1
	durability := &persistenceView{
		headers: []session.Header{
			grandchild.Header(),
			unavailable.Header(),
			staleLiveHeader,
			corrupt.Header(),
			coldOneShot.Header(),
		},
		inspections: map[session.SessionID]sessionpersistence.Inspection{
			coldOneShot.ID(): inspectionOf(coldOneShot),
			corrupt.ID():     inspectionOf(corrupt),
			grandchild.ID():  inspectionOf(grandchild),
		},
		failures: map[session.SessionID]error{
			unavailable.ID(): errors.New("backend unavailable"),
		},
	}
	projectionSource := projectionRegistry(t)
	directory := New()
	if err := directory.Enable(
		sessionList{
			conversations: []session.Context{
				creationWindow,
				liveContinuable,
			},
		},
		durability,
		projectionSource,
		nil,
	); err != nil {
		t.Fatal(err)
	}

	entries, err := directory.ListChildren(context.Background(), rootID)
	if err != nil {
		t.Fatal(err)
	}
	want := []subagent.ChildEntry{
		subagent.OneShotChildEntry{
			ID:          coldOneShot.ID(),
			Label:       stringPointer(oneShotLabel),
			Activity:    subagent.ActivityInactive,
			HasChildren: true,
		},
		subagent.DiagnosticEntry{
			ID:     corrupt.ID(),
			Reason: subagent.DiagnosticCorrupt,
		},
		subagent.ContinuableChildEntry{
			ID:          liveContinuable.ID(),
			Label:       "review",
			Activity:    subagent.ActivityRunning,
			HasChildren: false,
		},
		subagent.DiagnosticEntry{
			ID:     unavailable.ID(),
			Reason: subagent.DiagnosticUnavailable,
		},
	}
	if !reflect.DeepEqual(entries, want) {
		t.Fatalf("entries = %#v, want %#v", entries, want)
	}
}

func TestListDescendantsTraversesOrdinaryAndOneShotSessions(t *testing.T) {
	t.Parallel()
	rootID := session.SessionID("root")
	ordinary := newDirectorySession(t, "ordinary", rootID, 1, nil)
	oneShot := newDirectorySession(
		t,
		"one-shot",
		ordinary.ID(),
		2,
		subagent.OneShotDescriptor{
			Provider: "spawn",
		},
	)
	continuable := newDirectorySession(
		t,
		"continuable",
		oneShot.ID(),
		3,
		subagent.ContinuableDescriptor{
			Provider: "fork",
			Label:    "deep",
		},
	)
	ordinaryHeader := ordinary.Header()
	ordinaryHeader.Origin = ""
	durability := &persistenceView{
		headers: []session.Header{
			continuable.Header(),
			ordinaryHeader,
			oneShot.Header(),
		},
		inspections: map[session.SessionID]sessionpersistence.Inspection{
			oneShot.ID():     inspectionOf(oneShot),
			continuable.ID(): inspectionOf(continuable),
		},
	}
	directory := New()
	if err := directory.Enable(
		sessionList{},
		durability,
		projectionRegistry(t),
		nil,
	); err != nil {
		t.Fatal(err)
	}

	entries, err := directory.ListDescendants(context.Background(), rootID)
	if err != nil {
		t.Fatal(err)
	}
	want := []subagent.DescendantEntry{
		{
			Entry: subagent.OneShotChildEntry{
				ID:          oneShot.ID(),
				Activity:    subagent.ActivityInactive,
				HasChildren: true,
			},
			ParentID: ordinary.ID(),
			Depth:    2,
		},
		{
			Entry: subagent.ContinuableChildEntry{
				ID:          continuable.ID(),
				Label:       "deep",
				Activity:    subagent.ActivityInactive,
				HasChildren: false,
			},
			ParentID: oneShot.ID(),
			Depth:    3,
		},
	}
	if !reflect.DeepEqual(entries, want) {
		t.Fatalf("entries = %#v, want %#v", entries, want)
	}
}

func TestChildDirectoryReportsMissingCapabilitiesAndCancellation(t *testing.T) {
	t.Parallel()
	directory := New()
	if err := directory.Enable(sessionList{}, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	_, err := directory.ListChildren(context.Background(), "root")
	assertSubagentCode(t, err, subagent.ErrorControlProjectionsUnavailable)
	directory.Disable()
	if err = directory.Enable(nil, nil, projectionRegistry(t), nil); err != nil {
		t.Fatal(err)
	}
	_, err = directory.ListChildren(context.Background(), "root")
	assertSubagentCode(t, err, subagent.ErrorControlSessionStoreUnavailable)
	cancelledContext, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = directory.ListChildren(cancelledContext, "root")
	assertSubagentCode(t, err, subagent.ErrorCancelled)
}

func newDirectorySession(
	t *testing.T,
	identifier session.SessionID,
	parentID session.SessionID,
	createdAt int64,
	descriptor subagent.Descriptor,
) session.Context {
	t.Helper()
	conversation, err := session.New(
		identifier,
		session.CreateOptions{
			Metadata: session.Metadata{
				CreatedAt:     int64Pointer(createdAt),
				ParentSession: sessionIDPointer(parentID),
				Origin:        session.OriginSubagent,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if descriptor == nil {
		return conversation
	}
	descriptorData, err := subagent.SnapshotDescriptor(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	{
		var committedEvent session.Event
		var writeErr error
		draft, draftErr := session.NewEventDraft(subagent.DescriptorEvent,
			descriptorData)
		writeErr = draftErr
		if draftErr == nil {
			receipt, commitErr := conversation.Commit(context.Background(), session.Batch(draft))
			writeErr = commitErr
			if commitErr == nil {
				committedEvent = receipt.Events[0]
			}
		}
		if _, err = committedEvent, writeErr; err != nil {
			t.Fatal(err)
		}
	}
	return conversation
}

func projectionRegistry(t *testing.T) *sessionprojection.DriveRegistry {
	t.Helper()
	registry := sessionprojection.NewDriveRegistry()
	for _, unit := range subagentprojection.Units() {
		if _, err := registry.Register(unit); err != nil {
			t.Fatal(err)
		}
	}
	return registry
}

func inspectionOf(conversation session.Context) sessionpersistence.Inspection {
	return sessionpersistence.Inspection{
		Header: conversation.Header(),
		Events: conversation.Events(),
	}
}

func assertSubagentCode(
	t *testing.T,
	err error,
	want subagent.ErrorCode,
) {
	t.Helper()
	var problem *subagent.Error
	if !errors.As(err, &problem) || problem.Code != want {
		t.Fatalf("error = %v, want Subagent code %q", err, want)
	}
}

func sessionIDPointer(value session.SessionID) *session.SessionID {
	return &value
}

func int64Pointer(value int64) *int64 {
	return &value
}

func stringPointer(value string) *string {
	return &value
}
