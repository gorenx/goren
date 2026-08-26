package sessionapi

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	api "github.com/gorenx/goren/apiproxy"
	"github.com/gorenx/goren/session"
	sesspersist "github.com/gorenx/goren/session/persistence"
	sessionprojection "github.com/gorenx/goren/session/projection"
)

type historyCache struct {
	snapshot  sessionprojection.Snapshot
	coldCalls int
}

func (cache *historyCache) CachedSnapshot(
	session.Header,
) (*sessionprojection.Snapshot, error) {
	return nil, nil
}

func (cache *historyCache) ColdSnapshot(
	context.Context,
	session.SessionID,
) (sessionprojection.Snapshot, error) {
	cache.coldCalls++
	return cache.snapshot, nil
}

type historyPersistence struct {
	*windowPersistence
	inspectCalls int
}

func (source *historyPersistence) Inspect(
	context.Context,
	session.SessionID,
) (sesspersist.Inspection, error) {
	source.inspectCalls++
	return sesspersist.Inspection{}, errors.New("unexpected full inspection")
}

func TestColdHistoryTailUsesProjectionCutAndExcludesConcurrentAppend(t *testing.T) {
	events := make([]session.Event, 5)
	for index := range events {
		events[index] = session.Event{
			Type:      "extension/history-cache",
			Seq:       int64(index),
			Time:      int64(index + 1),
			Data:      json.RawMessage(`{}`),
			Ignorable: true,
		}
	}
	header := session.Header{
		Version: session.FormatVersion,
		ID:      "cold-history-cache",
	}
	durability := &historyPersistence{
		windowPersistence: &windowPersistence{
			header: header,
			events: events,
		},
	}
	cache := &historyCache{
		snapshot: sessionprojection.Snapshot{
			AsOfSeq: 3,
			Values: sessionprojection.Values{
				"title": json.RawMessage(`"stable"`),
			},
		},
	}
	reader := &sessionReader{
		sessions:    listLiveStore{},
		persistence: durability,
		cache:       cache,
	}
	value, err := reader.coldHistoryTail(
		context.Background(),
		header.ID,
		defaultHistoryMessages,
	)
	if err != nil {
		t.Fatal(err)
	}
	if cache.coldCalls != 1 || durability.inspectCalls != 0 || len(value.Events) != 4 ||
		value.Events[len(value.Events)-1].Event.Seq != 3 || value.HasMore ||
		value.Projections == nil || value.Projections.AsOfSeq != 3 {
		t.Fatalf(
			"history = %#v, cache calls = %d, Inspect calls = %d",
			value,
			cache.coldCalls,
			durability.inspectCalls,
		)
	}
}

func TestOlderColdHistoryPageDoesNotUseProjectionCache(t *testing.T) {
	events := make([]session.Event, 4)
	for index := range events {
		events[index] = session.Event{
			Type:      "extension/history-older",
			Seq:       int64(index),
			Time:      int64(index + 1),
			Data:      json.RawMessage(`{}`),
			Ignorable: true,
		}
	}
	header := session.Header{
		Version: session.FormatVersion,
		ID:      "older-history",
	}
	cache := &historyCache{}
	reader := &sessionReader{
		sessions: listLiveStore{},
		persistence: &historyPersistence{
			windowPersistence: &windowPersistence{
				header: header,
				events: events,
			},
		},
		cache: cache,
	}
	beforeSeq := int64(3)
	_, err := reader.History(
		context.Background(),
		api.Request[api.SessionHistoryRequest]{
			Payload: api.SessionHistoryRequest{
				SessionID: api.SessionID(header.ID),
				BeforeSeq: &beforeSeq,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if cache.coldCalls != 0 {
		t.Fatalf("cache calls = %d", cache.coldCalls)
	}
}
