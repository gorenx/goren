package sessionapi

import (
	"encoding/json"
	"testing"

	"github.com/gorenx/goren/session"
)

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
	page, hasMore, err := historyPage(events, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !hasMore || len(page) != 3 || page[0].Event.Seq != 3 || page[2].Event.Seq != 6 {
		t.Fatalf("page = %#v, hasMore = %t", page, hasMore)
	}
}
