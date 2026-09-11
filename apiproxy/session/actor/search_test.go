package sessionadapter_test

import (
	"context"
	"errors"
	"github.com/gorenx/goren/agentmessage"
	sessionadapter "github.com/gorenx/goren/apiproxy/session/actor"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/query"
	queryactor "github.com/gorenx/goren/session/query/actor"
	"github.com/gorenx/goren/session/query/sqlite"
	"github.com/gorenx/goren/session/title"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
	"path/filepath"
	"testing"
	"time"
)

func TestSearchClientUsesObservedQueryIndex(t *testing.T) {
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
	observations := &searchSources{}
	owner, err := queryactor.New(backend, query.Config{}, observations)
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
	target, err := system.Spawn(ctx, "query", owner, goakt.WithStashing())
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

	observations.items = []query.Source{{
		Log: query.LogSnapshot{
			Header: conversation.Header(),
			Events: conversation.Events(),
		},
		Live: true,
	}}
	endpoint, err := sessionadapter.NewSearch(target, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	matches, err := endpoint.SearchSessions(ctx, query.SearchSessionsRequest{
		Text: "alpha",
	})
	if err != nil || len(matches.Items) != 1 {
		t.Fatalf("search: %+v %v", matches, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = endpoint.SearchSessions(canceled, query.SearchSessionsRequest{
		Text: "alpha",
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled search: %v", err)
	}
}

type searchSources struct{ items []query.Source }

func (source *searchSources) ReadSources(ctx context.Context) ([]query.Source, error) {
	return source.items, ctx.Err()
}
