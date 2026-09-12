package sessionapi

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gorenx/goren/session"
	sesspersist "github.com/gorenx/goren/session/persistence"
	sessproj "github.com/gorenx/goren/session/projection"
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
	conversation *Snapshot
}

func (source listLiveStore) List(context.Context) ([]Snapshot, error) {
	if source.conversation == nil {
		return nil, nil
	}
	return []Snapshot{*source.conversation}, nil
}
func (source listLiveStore) Read(_ context.Context, identifier session.SessionID) (*Snapshot, error) {
	if source.conversation == nil || source.conversation.Header.ID != identifier {
		return nil, nil
	}
	return source.conversation, nil
}

type listPersistence struct {
	History
	headers      []session.Header
	inspectCalls int
}

func (source *listPersistence) List(
	_ context.Context,
	page sesspersist.SessionPage,
) (sesspersist.HeaderPage, error) {
	start := 0
	if page.Cursor != nil {
		for index, header := range source.headers {
			if header.CreatedAt == page.Cursor.CreatedAt && header.ID == page.Cursor.ID {
				start = index + 1
				break
			}
		}
	}
	end := min(len(source.headers), start+int(page.Limit))
	result := sesspersist.HeaderPage{
		Headers: append([]session.Header(nil), source.headers[start:end]...),
	}
	if end < len(source.headers) && end > start {
		last := source.headers[end-1]
		result.NextCursor = &sesspersist.SessionCursor{
			CreatedAt: last.CreatedAt,
			ID:        last.ID,
		}
	}
	return result, nil
}

func (source *listPersistence) Inspect(
	context.Context,
	session.SessionID,
) (sesspersist.Inspection, error) {
	source.inspectCalls++
	return sesspersist.Inspection{}, errors.New("unexpected full inspection")
}

type listCache struct {
	snapshot     sessproj.Snapshot
	found        bool
	err          error
	coldSnapshot sessproj.Snapshot
	coldErr      error
	coldCalls    int
}

func (cache *listCache) CachedSnapshot(
	context.Context,
	session.Header,
) (*sessproj.Snapshot, error) {
	if cache.err != nil || !cache.found {
		return nil, cache.err
	}
	projectionSnapshot := cache.snapshot
	return &projectionSnapshot, nil
}

func (cache *listCache) ColdSnapshot(
	context.Context,
	session.SessionID,
) (sessproj.Snapshot, error) {
	cache.coldCalls++
	return cache.coldSnapshot, cache.coldErr
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
		sessions:    listLiveStore{},
		persistence: durability,
		cache: &listCache{
			found: true,
			snapshot: sessproj.Snapshot{
				AsOfSeq: 5,
				Values: sessproj.Values{
					sessionListMetadataKey: metadataValue,
					"title":                json.RawMessage(`"cached"`),
				},
			},
		},
	}
	page, err := reader.visibleSessionSummaries(
		context.Background(),
		sesspersist.SessionPage{
			Limit: defaultSessionListPageSize,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	items := page.items
	if durability.inspectCalls != 0 {
		t.Fatalf("Inspect calls = %d", durability.inspectCalls)
	}
	if len(items) != 1 || items[0].UpdatedAt != 25 || items[0].Blank ||
		items[0].Projections == nil || items[0].Projections.AsOfSeq != 5 {
		t.Fatalf("items = %#v", items)
	}
}

func TestColdSessionListCacheMissRestoresProjection(t *testing.T) {
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
	metadataValue, err := encodeSessionListMetadata(sessionListMetadataState{
		Blank:        false,
		LastPromptAt: sequencePointer(25),
	})
	if err != nil {
		t.Fatal(err)
	}
	cache := &listCache{
		coldSnapshot: sessproj.Snapshot{
			AsOfSeq: 5,
			Values: sessproj.Values{
				sessionListMetadataKey: metadataValue,
				"title":                json.RawMessage(`"restored"`),
			},
		},
	}
	reader := &sessionReader{
		sessions:    listLiveStore{},
		persistence: durability,
		cache:       cache,
	}
	page, err := reader.visibleSessionSummaries(
		context.Background(),
		sesspersist.SessionPage{
			Limit: defaultSessionListPageSize,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	items := page.items
	if durability.inspectCalls != 0 || cache.coldCalls != 1 || len(items) != 1 ||
		items[0].Blank || items[0].UpdatedAt != 25 || items[0].Projections == nil ||
		string(items[0].Projections.Values["title"]) != `"restored"` {
		t.Fatalf(
			"items = %#v, Inspect calls = %d, cold calls = %d",
			items,
			durability.inspectCalls,
			cache.coldCalls,
		)
	}
}

func TestSessionListCursorContinuesWithoutRepeatingHeaders(t *testing.T) {
	workingDirectory := t.TempDir()
	durability := &listPersistence{
		headers: []session.Header{
			{
				Version:   session.FormatVersion,
				ID:        "newest",
				CreatedAt: 30,
				CWD:       &workingDirectory,
			},
			{
				Version:   session.FormatVersion,
				ID:        "middle",
				CreatedAt: 20,
				CWD:       &workingDirectory,
			},
			{
				Version:   session.FormatVersion,
				ID:        "oldest",
				CreatedAt: 10,
				CWD:       &workingDirectory,
			},
		},
	}
	reader := &sessionReader{
		sessions:    listLiveStore{},
		persistence: durability,
	}
	first, err := reader.visibleSessionSummaries(
		context.Background(),
		sesspersist.SessionPage{
			Limit: 2,
		},
	)
	if err != nil || first.nextCursor == nil {
		t.Fatalf("first page = (%#v, %v)", first, err)
	}
	encoded, err := encodeSessionListCursor(first.nextCursor)
	if err != nil || encoded == nil {
		t.Fatalf("encoded cursor = (%#v, %v)", encoded, err)
	}
	decoded, err := decodeSessionListPage(ListRequest{
		Cursor: encoded,
		Limit:  sequencePointer(2),
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := reader.visibleSessionSummaries(context.Background(), decoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.items) != 2 || first.items[0].SessionID != "newest" || first.items[1].SessionID != "middle" ||
		len(second.items) != 1 || second.items[0].SessionID != "oldest" || second.nextCursor != nil {
		t.Fatalf("pages = (%#v, %#v)", first, second)
	}
}

func TestSessionListIncludesChildrenWithoutCWD(t *testing.T) {
	for _, live := range []bool{false, true} {
		name := "stored"
		if live {
			name = "live"
		}
		t.Run(name, func(t *testing.T) {
			parent := session.SessionID("parent")
			header := session.Header{
				Version:       session.FormatVersion,
				ID:            "child",
				ParentSession: &parent,
				Origin:        session.OriginSubagent,
			}
			source := listLiveStore{}
			if live {
				source.conversation = &Snapshot{
					Header: header,
				}
			}
			reader := &sessionReader{
				sessions: source,
				persistence: &listPersistence{
					headers: []session.Header{header, {
						Version: session.FormatVersion,
						ID:      "headless-root",
					}},
				},
			}
			page, err := reader.visibleSessionSummaries(context.Background(), sesspersist.SessionPage{
				Limit: defaultSessionListPageSize,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(page.items) != 1 || page.items[0].SessionID != "child" || string(page.items[0].ParentSessionID) != string(parent) {
				t.Fatalf("child listing = %#v", page.items)
			}
		})
	}
}
