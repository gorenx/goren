package titleactor_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	"github.com/gorenx/goren/session/persistence/sqlite"
	sessionregistry "github.com/gorenx/goren/session/registry"
	"github.com/gorenx/goren/session/title"
	titleactor "github.com/gorenx/goren/session/title/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
	"github.com/tochemey/goakt/v4/reentrancy"
)

type observer struct {
	ready  chan struct{}
	titles chan session.Event
}

func (*observer) PreStart(*goakt.Context) error { return nil }
func (*observer) PostStop(*goakt.Context) error { return nil }
func (sink *observer) Receive(rc *goakt.ReceiveContext) {
	switch input := rc.Message().(type) {
	case *goakt.PostStart:
		rc.Tell(rc.ActorSystem().TopicActor(), goakt.NewSubscribe(session.AppendedEventName))
	case *goakt.SubscribeAck, titleactor.Subscribed:
		sink.ready <- struct{}{}
	case sessionactor.CommitNotice:
		if input.Event.Type == title.TitleEventName {
			sink.titles <- input.Event.Clone()
		}
	}
}

func TestHumanMessageGeneratesTitleWithoutMainRequest(t *testing.T) {
	ctx := context.Background()
	backend, err := sqlite.Open(ctx, sqlite.Config{
		Path:        filepath.Join(t.TempDir(), "sessions.sqlite"),
		JournalMode: sqlite.JournalWAL,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := backend.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	system, err := goakt.NewActorSystem("title-subscription", goakt.WithPubSub(), goakt.WithLogger(log.DiscardLogger))
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
	replies := make(chan response, 8)
	driver, err := system.Spawn(ctx, "requester", &requester{
		replies: replies,
	}, goakt.WithReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
	if err != nil {
		t.Fatal(err)
	}
	registry, err := goakt.GrainOf[*sessionregistry.RegistryGrain](ctx, system, sessionregistry.RegistryName, goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	reserved := request[sessionregistry.Completion](t, driver, replies, registry, sessionregistry.Reserve{
		SessionID:  "conversation",
		CreationID: "creation",
	})
	if reserved.Err != nil {
		t.Fatal(reserved.Err)
	}
	conversation, err := goakt.GrainOf[*sessionactor.Grain](ctx, system, sessionactor.RuntimeName("conversation"), goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	initialized := request[sessionactor.Initialized](t, driver, replies, conversation, sessionactor.Initialize{
		SessionID:  "conversation",
		Authority:  registry,
		CreationID: "creation",
		Storage:    backend,
	})
	if initialized.Err != nil {
		t.Fatal(initialized.Err)
	}
	identity, err := goakt.GrainOf[*titleactor.Grain](ctx, system, titleactor.RuntimeName("conversation"), goakt.WithLongLivedGrain())
	if err != nil {
		t.Fatal(err)
	}
	bound := request[titleactor.Completion](t, driver, replies, identity, titleactor.Initialize{
		CreationID: "creation",
		Session:    conversation,
		Header:     initialized.Header,
		Provider:   &provider{},
		Config: title.Config{
			MaxTitleBytes:    80,
			FallbackMaxBytes: 40,
			FallbackMaxWords: 3,
		},
	})
	if bound.Err != nil {
		t.Fatal(bound.Err)
	}
	ready := make(chan struct{}, 2)
	titles := make(chan session.Event, 8)
	sink, err := system.Spawn(ctx, "observer", &observer{
		ready:  ready,
		titles: titles,
	})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := titleactor.NewSubscriber(identity, "creation", sink)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := system.Spawn(ctx, "subscriber", listener, goakt.WithReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll)))); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 2; index++ {
		select {
		case <-ready:
		case <-time.After(3 * time.Second):
			t.Fatal("subscription not ready")
		}
	}
	message, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
		Content: []agentmessage.ContentBlock{agentmessage.NewTextBlock("human prompt")},
		Source: agentmessage.UserMessageSource{
			Kind: "user",
		},
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
	committed := request[sessionactor.Committed](t, driver, replies, conversation, sessionactor.Commit{
		CreationID: "creation",
		Plan:       session.Batch(draft),
	})
	if committed.Err != nil {
		t.Fatal(committed.Err)
	}
	deadline := time.After(3 * time.Second)
	generated := false
	for !generated {
		select {
		case entry := <-titles:
			var data title.EventData
			if err := json.Unmarshal(entry.Data, &data); err != nil {
				t.Fatal(err)
			}
			generated = data.Source.SourceKind() == "provider"
		case <-deadline:
			t.Fatal("human event did not generate a title")
		}
	}
	captured := request[sessionactor.View](t, driver, replies, conversation, sessionactor.Read{
		CreationID: "creation",
	})
	if captured.Err != nil {
		t.Fatal(captured.Err)
	}
	for _, entry := range captured.Snapshot.Events {
		if entry.Type == session.RequestHeaderEventName || entry.Type == session.StepStartEventName {
			t.Fatal("title required a main request")
		}
	}
}
