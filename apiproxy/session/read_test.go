package sessionapi

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gorenx/goren/session"
	sesspersist "github.com/gorenx/goren/session/persistence"
)

type windowPersistence struct {
	sesspersist.Persistence
	header session.Header
	events []session.Event
	reads  int
}

func (storage *windowPersistence) ReadEventsBefore(
	_ context.Context,
	_ session.SessionID,
	beforeSeq *int64,
	maxEvents int64,
) (sesspersist.EventWindow, error) {
	storage.reads++
	end := len(storage.events)
	if beforeSeq != nil && *beforeSeq < int64(end) {
		end = int(*beforeSeq)
	}
	start := max(0, end-int(maxEvents))
	window := append([]session.Event(nil), storage.events[start:end]...)
	for left, right := 0, len(window)-1; left < right; left, right = left+1, right-1 {
		window[left], window[right] = window[right], window[left]
	}
	return sesspersist.EventWindow{
		Header:     storage.header,
		Events:     window,
		HasEarlier: start > 0,
	}, nil
}

func TestHistoryPageCutsAtAppendMessageGroup(t *testing.T) {
	t.Parallel()
	appendOperation := session.SurfaceAppend()
	sources := []int64{3, 4}
	events := []session.Event{
		{Type: "turn/start", Seq: 0, Time: 1, Data: json.RawMessage(`{}`)},
		{Type: session.UserMessageEventName, Seq: 1, Time: 2, Data: json.RawMessage(`{}`), SurfaceOp: &appendOperation},
		{Type: "assistant/chunk", Seq: 2, Time: 3, Data: json.RawMessage(`{}`)},
		{Type: "assistant/chunk", Seq: 3, Time: 4, Data: json.RawMessage(`{}`)},
		{Type: session.AssistantMessageEventName, Seq: 5, Time: 6, Data: json.RawMessage(`{}`), SourceEventSeqs: &sources, SurfaceOp: &appendOperation},
		{Type: "turn/end", Seq: 6, Time: 7, Data: json.RawMessage(`{}`)},
	}
	window, err := historyPage(events, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !window.hasMore || len(window.events) != 3 ||
		window.events[0].Event.Seq != 3 || window.events[2].Event.Seq != 6 {
		t.Fatalf("window = %#v", window)
	}
}

func TestColdHistoryPageScansBackwardUntilTheWholeMessageGroupIsAvailable(t *testing.T) {
	t.Parallel()
	appendOperation := session.SurfaceAppend()
	entries := make([]session.Event, 1100)
	for sequence := range entries {
		entries[sequence] = session.Event{
			Type:      "extension/history-fixture",
			Seq:       int64(sequence),
			Time:      int64(sequence + 1),
			Data:      json.RawMessage(`{}`),
			Ignorable: true,
		}
	}
	entries[100] = session.Event{
		Type:      session.UserMessageEventName,
		Seq:       100,
		Time:      101,
		Data:      json.RawMessage(`{}`),
		SurfaceOp: &appendOperation,
	}
	sources := []int64{400, 799}
	entries[800] = session.Event{
		Type:            session.AssistantMessageEventName,
		Seq:             800,
		Time:            801,
		Data:            json.RawMessage(`{}`),
		SourceEventSeqs: &sources,
		SurfaceOp:       &appendOperation,
	}
	storage := &windowPersistence{
		header: session.Header{
			Version: session.FormatVersion,
			ID:      "cold-history",
		},
		events: entries,
	}
	reader := &sessionReader{
		persistence: storage,
	}

	window, err := reader.coldHistoryPage(
		context.Background(),
		storage.header.ID,
		nil,
		1,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !window.hasMore || storage.reads != 2 || len(window.events) != 700 ||
		window.events[0].Event.Seq != 400 ||
		window.events[len(window.events)-1].Event.Seq != 1099 {
		t.Fatalf(
			"cold page = (len=%d, first=%d, last=%d, hasMore=%t, reads=%d)",
			len(window.events),
			window.events[0].Event.Seq,
			window.events[len(window.events)-1].Event.Seq,
			window.hasMore,
			storage.reads,
		)
	}
}
