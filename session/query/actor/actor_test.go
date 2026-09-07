package queryactor_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/query"
	queryactor "github.com/gorenx/goren/session/query/actor"
	"github.com/gorenx/goren/session/query/sqlite"
	"github.com/gorenx/goren/session/title"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
	"github.com/tochemey/goakt/v4/reentrancy"
)

type command struct {
	payload any
}

type response struct {
	value any
	err   error
}

type requester struct {
	target  *goakt.PID
	replies chan response
}

func (*requester) PreStart(*goakt.Context) error { return nil }
func (*requester) PostStop(*goakt.Context) error { return nil }
func (driver *requester) Receive(rc *goakt.ReceiveContext) {
	if input, ok := rc.Message().(command); ok {
		replies := driver.replies
		rc.Request(driver.target, input.payload).Then(func(value any, err error) {
			replies <- response{
				value: value,
				err:   err,
			}
		})
	}
}

func request[T any](t *testing.T, driver *goakt.PID, replies <-chan response, payload any) T {
	t.Helper()
	if err := goakt.Tell(context.Background(), driver, command{payload: payload}); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-replies:
		if result.err != nil {
			t.Fatal(result.err)
		}
		value, ok := result.value.(T)
		if !ok {
			t.Fatalf("unexpected reply %T", result.value)
		}
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("request timed out")
		var empty T
		return empty
	}
}

func TestActorQueriesObservedSourcesThroughFrameworkRequests(t *testing.T) {
	ctx := context.Background()
	backend, err := sqlite.Open(ctx, sqlite.Config{
		Path: filepath.Join(t.TempDir(), "query.sqlite"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := backend.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	owner, err := queryactor.New(backend, query.Config{})
	if err != nil {
		t.Fatal(err)
	}
	system, err := goakt.NewActorSystem("query-test", goakt.WithLogger(log.DiscardLogger))
	if err != nil {
		t.Fatal(err)
	}
	if err := system.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := system.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	target, err := system.Spawn(ctx, "query", owner)
	if err != nil {
		t.Fatal(err)
	}
	replies := make(chan response, 4)
	driver, err := system.Spawn(ctx, "requester", &requester{
		target:  target,
		replies: replies,
	}, goakt.WithReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := session.NewLog("conversation", session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	message, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
		Content: []agentmessage.ContentBlock{agentmessage.NewTextBlock("alpha question")},
		Source:  agentmessage.UserMessageSource{Kind: "user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	draft, err := session.NewSurfaceEventDraft(session.UserMessageAdded, message, session.SurfaceIntent{
		Operation: session.SurfaceAppend(),
	})
	if err != nil {
		t.Fatal(err)
	}
	named, err := session.NewEventDraft(title.TitleSet, title.EventData{
		Title:       "Named",
		MessageSeqs: []int64{},
		Source:      title.UserSource{Kind: "user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conversation.Commit(ctx, session.Batch(draft, named)); err != nil {
		t.Fatal(err)
	}
	accepted := request[queryactor.Completion](t, driver, replies, queryactor.Observe{
		Sources: []query.Source{{
			Log: query.LogSnapshot{
				Header: conversation.Header(),
				Events: conversation.Events(),
			},
			Live: true,
		}},
	})
	if accepted.Err != nil {
		t.Fatal(accepted.Err)
	}
	listed := request[queryactor.Sessions](t, driver, replies, queryactor.ListSessions{})
	if listed.Err != nil || len(listed.Items) != 1 {
		t.Fatalf("sessions = %#v", listed)
	}
	captured := request[queryactor.Log](t, driver, replies, queryactor.ReadSession{SessionID: "conversation"})
	if captured.Err != nil || len(captured.Value.Events) != 2 {
		t.Fatalf("log = %#v", captured)
	}
	selected := request[queryactor.Title](t, driver, replies, queryactor.ReadTitle{SessionID: "conversation"})
	if selected.Err != nil || selected.Value == nil || selected.Value.Title != "Named" {
		t.Fatalf("title = %#v", selected)
	}
	titles := request[queryactor.Titles](t, driver, replies, queryactor.ReadTitles{
		SessionIDs: []session.SessionID{"conversation", "missing", "conversation"},
	})
	if titles.Err != nil || len(titles.Items) != 2 || titles.Items[0].Err != nil || titles.Items[1].Err == nil {
		t.Fatalf("titles = %#v", titles)
	}
	events := request[queryactor.Events](t, driver, replies, queryactor.ListEvents{SessionID: "conversation"})
	if events.Err != nil || len(events.Items) != 2 {
		t.Fatalf("events = %#v", events)
	}
	surface := request[queryactor.Surface](t, driver, replies, queryactor.ReadSurface{SessionID: "conversation"})
	if surface.Err != nil || len(surface.Value.Events) != 1 {
		t.Fatalf("surface = %#v", surface)
	}
	window := request[queryactor.Event](t, driver, replies, queryactor.ReadEvent{
		Request: query.EventReadRequest{
			SessionID: "conversation",
			Seq:       0,
			After:     1,
		},
	})
	if window.Err != nil || len(window.Value.Events) != 2 {
		t.Fatalf("window = %#v", window)
	}
	documents := request[queryactor.Documents](t, driver, replies, queryactor.FilterEvents{
		SessionID: "conversation",
		Text:      "alpha",
	})
	if documents.Err != nil || len(documents.Items) != 1 {
		t.Fatalf("documents = %#v", documents)
	}
	lineage := request[queryactor.Lineage](t, driver, replies, queryactor.TraceSession{SessionID: "conversation"})
	if lineage.Err != nil || !lineage.Value.Complete {
		t.Fatalf("lineage = %#v", lineage)
	}
	matches := request[queryactor.SessionMatches](t, driver, replies, queryactor.SearchSessions{
		Request: query.SearchSessionsRequest{Text: "alpha"},
	})
	if matches.Err != nil || len(matches.Page.Items) != 1 {
		t.Fatalf("matches = %#v", matches)
	}
	hits := request[queryactor.EventMatches](t, driver, replies, queryactor.SearchEvents{
		Request: query.SearchEventsRequest{
			SessionID: "conversation",
			Text:      "alpha",
		},
	})
	if hits.Err != nil || len(hits.Page.Items) != 1 {
		t.Fatalf("event matches = %#v", hits)
	}
	if err := system.Kill(ctx, target.Name()); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Snapshot(ctx); err != nil {
		t.Fatalf("actor closed shared index: %v", err)
	}
}
