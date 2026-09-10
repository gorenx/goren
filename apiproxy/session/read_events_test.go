package sessionapi

import (
	"context"
	"errors"
	"testing"

	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/persistence"
)

type eventHistory struct {
	History
	calls    int
	snapshot persistence.Inspection
	failure  error
}

func (source *eventHistory) Inspect(context.Context, session.SessionID) (persistence.Inspection, error) {
	source.calls++
	return source.snapshot, source.failure
}

func TestReadEventsSelectsOneIdentityCheckedSnapshot(t *testing.T) {
	for _, live := range []bool{false, true} {
		archive := &eventHistory{
			snapshot: persistence.Inspection{
				Header: session.Header{
					ID: "conversation",
				},
				Events: []session.Event{{
					Type: "durable",
				}},
			},
		}
		sessions := listLiveStore{}
		if live {
			sessions.conversation = &Snapshot{
				Header: session.Header{
					ID: "conversation",
				},
				Events: []session.Event{{
					Type: "live",
				}},
			}
		}
		view := &sessionReader{
			sessions:    sessions,
			persistence: archive,
		}
		entries, err := view.ReadEvents(context.Background(), "conversation")
		if err != nil || len(entries) != 1 {
			t.Fatalf("read: %+v, %v", entries, err)
		}
		if live && (entries[0].Type != "live" || archive.calls != 0) {
			t.Fatal("mixed live and durable logs")
		}
		if !live && (entries[0].Type != "durable" || archive.calls != 1) {
			t.Fatal("missing cold read")
		}
	}
	archive := &eventHistory{
		snapshot: persistence.Inspection{
			Header: session.Header{
				ID: "other",
			},
		},
	}
	view := &sessionReader{
		sessions:    listLiveStore{},
		persistence: archive,
	}
	if _, err := view.ReadEvents(context.Background(), "conversation"); err == nil {
		t.Fatal("wrong identity accepted")
	}
	archive.failure = errors.New("unavailable")
	if _, err := view.ReadEvents(context.Background(), "conversation"); err != archive.failure {
		t.Fatal("read failure hidden")
	}
}
