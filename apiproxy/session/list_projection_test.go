package sessionapi

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
	sesspersist "github.com/gorenx/goren/session/persistence"
	sessionprojection "github.com/gorenx/goren/session/projection"
)

func TestSessionListMetadataProjectionTracksMonotonicSummaryFacts(t *testing.T) {
	unit := SessionListMetadataUnit()
	state, err := unit.InitialState()
	if err != nil {
		t.Fatal(err)
	}
	transition, err := unit.ApplyState(
		state,
		session.Event{
			Type: session.TurnStartEventName,
			Seq:  0,
			Time: 10,
			Data: json.RawMessage(`{}`),
		},
	)
	if err != nil || !transition.Changed {
		t.Fatalf("turn/start transition = (%s, %v, %v)", transition.State, transition.Changed, err)
	}
	transition, err = unit.ApplyState(
		transition.State,
		session.Event{
			Type: session.UserMessageEventName,
			Seq:  1,
			Time: 20,
			Data: json.RawMessage(`{"source":{"kind":"user"}}`),
		},
	)
	if err != nil || !transition.Changed {
		t.Fatalf("user/message transition = (%s, %v, %v)", transition.State, transition.Changed, err)
	}
	metadata, err := decodeSessionListMetadata(transition.State)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Blank || metadata.LastPromptAt == nil || *metadata.LastPromptAt != 20 {
		t.Fatalf("metadata = %#v", metadata)
	}
	unchanged, err := unit.ApplyState(
		transition.State,
		session.Event{
			Type: "extension/noop",
			Seq:  2,
			Time: 30,
			Data: json.RawMessage(`{}`),
		},
	)
	if err != nil || unchanged.Changed || string(unchanged.State) != string(transition.State) {
		t.Fatalf("noop transition = (%s, %v, %v)", unchanged.State, unchanged.Changed, err)
	}
}

type listLiveStore struct {
	session.LiveStore
	conversation session.Context
}

func (store listLiveStore) List() []session.Context {
	if store.conversation == nil {
		return nil
	}
	return []session.Context{store.conversation}
}

func (store listLiveStore) Get(identifier session.SessionID) (session.Context, bool) {
	if store.conversation == nil || store.conversation.ID() != identifier {
		return nil, false
	}
	return store.conversation, true
}

type listPersistence struct {
	sesspersist.Persistence
	headers      []session.Header
	inspectCalls int
}

func (source *listPersistence) List(context.Context) ([]session.Header, error) {
	return append([]session.Header(nil), source.headers...), nil
}

func (source *listPersistence) Inspect(
	context.Context,
	session.SessionID,
) (sesspersist.Inspection, error) {
	source.inspectCalls++
	return sesspersist.Inspection{}, errors.New("unexpected full inspection")
}

type listCache struct {
	snapshot sessionprojection.Snapshot
	found    bool
	err      error
}

func (cache listCache) CachedSnapshot(
	session.Header,
) (*sessionprojection.Snapshot, error) {
	if cache.err != nil || !cache.found {
		return nil, cache.err
	}
	projectionSnapshot := cache.snapshot
	return &projectionSnapshot, nil
}

func (listCache) ColdSnapshot(
	context.Context,
	session.SessionID,
) (sessionprojection.Snapshot, error) {
	return sessionprojection.Snapshot{}, errors.New("unexpected cold snapshot")
}

type emptyAgentRegistry struct {
	agent.Registry
}

func (emptyAgentRegistry) Get(session.SessionID) (agent.Agent, bool) {
	return nil, false
}

func TestColdSessionListUsesCachedProjectionWithoutInspect(t *testing.T) {
	workingDirectory := t.TempDir()
	header := session.Header{
		Version:   session.FormatVersion,
		ID:        "cold-list",
		CreatedAt: 10,
		CWD:       &workingDirectory,
	}
	metadataValue, err := encodeSessionListMetadata(sessionListMetadataState{
		Blank:        false,
		LastPromptAt: sequencePointer(25),
	})
	if err != nil {
		t.Fatal(err)
	}
	durability := &listPersistence{
		headers: []session.Header{header},
	}
	reader := &sessionReader{
		agents:      emptyAgentRegistry{},
		sessions:    listLiveStore{},
		persistence: durability,
		cache: listCache{
			found: true,
			snapshot: sessionprojection.Snapshot{
				AsOfSeq: 5,
				Values: sessionprojection.Values{
					sessionListMetadataKey: metadataValue,
					"title":                json.RawMessage(`"cached"`),
				},
			},
		},
	}
	items, err := reader.visibleSessionSummaries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if durability.inspectCalls != 0 {
		t.Fatalf("Inspect calls = %d", durability.inspectCalls)
	}
	if len(items) != 1 || items[0].UpdatedAt != 25 || items[0].Blank ||
		items[0].Projections == nil || items[0].Projections.AsOfSeq != 5 {
		t.Fatalf("items = %#v", items)
	}
}

func TestColdSessionListCacheMissUsesConservativeSummary(t *testing.T) {
	workingDirectory := t.TempDir()
	header := session.Header{
		Version:   session.FormatVersion,
		ID:        "cold-miss",
		CreatedAt: 10,
		CWD:       &workingDirectory,
	}
	durability := &listPersistence{
		headers: []session.Header{header},
	}
	reader := &sessionReader{
		agents:      emptyAgentRegistry{},
		sessions:    listLiveStore{},
		persistence: durability,
		cache:       listCache{},
	}
	items, err := reader.visibleSessionSummaries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if durability.inspectCalls != 0 || len(items) != 1 ||
		items[0].Blank || items[0].UpdatedAt != 10 {
		t.Fatalf("items = %#v, Inspect calls = %d", items, durability.inspectCalls)
	}
}
