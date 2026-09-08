package titleactor_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
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

type command struct {
	target  *goakt.GrainIdentity
	payload any
}

type response struct {
	value any
	err   error
}

type requester struct {
	replies chan response
}

func (*requester) PreStart(*goakt.Context) error { return nil }
func (*requester) PostStop(*goakt.Context) error { return nil }
func (driver *requester) Receive(rc *goakt.ReceiveContext) {
	if input, ok := rc.Message().(command); ok {
		replies := driver.replies
		rc.RequestGrain(input.target, input.payload).Then(func(value any, err error) {
			replies <- response{
				value: value,
				err:   err,
			}
		})
	}
}

func request[T any](t *testing.T, driver *goakt.PID, replies <-chan response, target *goakt.GrainIdentity, payload any) T {
	t.Helper()
	if err := goakt.Tell(context.Background(), driver, command{
		target:  target,
		payload: payload,
	}); err != nil {
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

func TestTitleGrainUsesSessionCommitsAndRejectsClosedCreation(t *testing.T) {
	ctx := context.Background()
	backend, err := sqlite.Open(ctx, sqlite.Config{
		Path:        filepath.Join(t.TempDir(), "session.sqlite"),
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
	system, err := goakt.NewActorSystem("title-test", goakt.WithLogger(log.DiscardLogger))
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
		CreationID: "first",
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
		CreationID: "first",
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
		CreationID: "first",
		Header:     initialized.Header,
		Session:    conversation,
		Config: title.Config{
			MaxTitleBytes:    80,
			FallbackMaxBytes: 40,
			FallbackMaxWords: 3,
		},
	})
	if bound.Err != nil {
		t.Fatal(bound.Err)
	}
	view := request[titleactor.View](t, driver, replies, identity, titleactor.Read{
		CreationID: "first",
	})
	if view.Err != nil || view.Title != nil {
		t.Fatalf("initial view = %#v", view)
	}
	message, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
		Content: []agentmessage.ContentBlock{agentmessage.NewTextBlock("one two three four")},
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
		CreationID: "first",
		Plan:       session.Batch(draft),
	})
	if committed.Err != nil {
		t.Fatal(committed.Err)
	}

	failed := request[titleactor.Updated](t, driver, replies, identity, titleactor.Generate{
		CreationID: "first",
		Provider: &provider{
			failure: errors.New("injected preparation failure"),
		},
	})
	if failed.Err == nil {
		t.Fatal("provider preparation failure was hidden")
	}
	fallback := request[titleactor.Updated](t, driver, replies, identity, titleactor.EnsureFallback{
		CreationID: "first",
	})
	if fallback.Err != nil || len(fallback.Result.Events) != 0 {
		t.Fatalf("fallback = %#v", fallback)
	}
	view = request[titleactor.View](t, driver, replies, identity, titleactor.Read{
		CreationID: "first",
	})
	if view.Err != nil || view.Title == nil || view.Title.Title != "one two three" {
		t.Fatalf("fallback view = %#v", view)
	}

	immediate := request[titleactor.Updated](t, driver, replies, identity, titleactor.Generate{
		CreationID: "first",
		Provider:   &provider{},
	})
	if immediate.Err != nil || len(immediate.Result.Events) != 1 {
		t.Fatalf("generated title = %#v", immediate)
	}

	pinned := request[titleactor.Updated](t, driver, replies, identity, titleactor.Rename{
		CreationID: "first",
		Title:      "pinned",
	})
	if pinned.Err != nil {
		t.Fatal(pinned.Err)
	}
	refreshed := request[titleactor.Updated](t, driver, replies, identity, titleactor.Generate{
		CreationID: "first",
		Refresh:    true,
	})
	if refreshed.Err != nil || len(refreshed.Result.Events) != 1 {
		t.Fatalf("fallback refresh = %#v", refreshed)
	}
	view = request[titleactor.View](t, driver, replies, identity, titleactor.Read{
		CreationID: "first",
	})
	if view.Err != nil || view.Title.Title != "one two three" || view.Title.Source.SourceKind() != "fallback" {
		t.Fatalf("refresh view = %#v", view)
	}
	repeated := request[titleactor.Updated](t, driver, replies, identity, titleactor.Generate{
		CreationID: "first",
		Refresh:    true,
	})
	if repeated.Err != nil || len(repeated.Result.Events) != 0 {
		t.Fatalf("automatic title was rewritten: %#v", repeated)
	}
	gate := make(chan struct{})
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(gate) }) })
	entered := make(chan struct{}, 1)
	if err := goakt.Tell(ctx, driver, command{
		target: identity,
		payload: titleactor.Generate{
			CreationID: "first",
			Provider: &provider{
				entered: entered,
				release: gate,
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("model call never started")
	}
	logged := request[sessionactor.View](t, driver, replies, conversation, sessionactor.Read{
		CreationID: "first",
	})
	if logged.Err != nil || logged.Snapshot.Events[len(logged.Snapshot.Events)-1].Type != "test/title-generation" {
		t.Fatalf("missing pre-dispatch record: %#v", logged)
	}
	renamed := request[titleactor.Updated](t, driver, replies, identity, titleactor.Rename{
		CreationID: "first",
		Title:      "  manual\tname ",
	})
	if renamed.Err != nil || len(renamed.Result.Events) != 1 {
		t.Fatalf("rename = %#v", renamed)
	}
	fallback = request[titleactor.Updated](t, driver, replies, identity, titleactor.EnsureFallback{
		CreationID: "first",
	})
	if fallback.Err != nil || len(fallback.Result.Events) != 0 {
		t.Fatalf("fallback overwrote rename: %#v", fallback)
	}
	view = request[titleactor.View](t, driver, replies, identity, titleactor.Read{
		CreationID: "first",
	})
	if view.Err != nil || view.Title.Title != "manual name" {
		t.Fatalf("rename view = %#v", view)
	}
	rejected := request[titleactor.Updated](t, driver, replies, identity, titleactor.Rename{
		CreationID: "wrong",
		Title:      "bad",
	})
	if rejected.Err == nil {
		t.Fatal("unknown creation accepted")
	}

	if err := goakt.Tell(ctx, driver, command{
		target: identity,
		payload: titleactor.Close{
			CreationID: "first",
		},
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-replies:
		t.Fatalf("close or generation completed before model returned: %#v", result)
	case <-time.After(40 * time.Millisecond):
	}
	release.Do(func() { close(gate) })
	closed, cancelled := false, false
	for index := 0; index < 2; index++ {
		select {
		case result := <-replies:
			if result.err != nil {
				t.Fatal(result.err)
			}
			switch value := result.value.(type) {
			case titleactor.Completion:
				if value.Err != nil {
					t.Fatal(value.Err)
				}
				closed = true
			case titleactor.Updated:
				if value.Err == nil {
					t.Fatal("cancelled generation committed")
				}
				cancelled = true
			default:
				t.Fatalf("unexpected completion %T", result.value)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("model drain did not complete")
		}
	}
	if !closed || !cancelled {
		t.Fatal("missing close or generation reply")
	}
	view = request[titleactor.View](t, driver, replies, identity, titleactor.Read{
		CreationID: "first",
	})
	if view.Err == nil {
		t.Fatal("closed creation read accepted")
	}
	stillLive := request[sessionactor.View](t, driver, replies, conversation, sessionactor.Read{
		CreationID: "first",
	})
	if stillLive.Err != nil {
		t.Fatalf("Title closed Session: %v", stillLive.Err)
	}
}

var requestEvent = session.DefineEvent[bool]("test/title-generation")

type provider struct {
	failure error
	entered chan struct{}
	release <-chan struct{}
}

func (*provider) ID() title.ProviderID               { return "test-provider" }
func (*provider) AutomaticMode() title.AutomaticMode { return title.AutomaticAllPrompts }
func (implementation *provider) Prepare(request title.ProviderRequest) (title.Generation, error) {
	if implementation.failure != nil {
		return nil, implementation.failure
	}
	draft, err := session.NewEventDraft(requestEvent, true)
	if err != nil {
		return nil, err
	}
	sequences := make([]int64, len(request.Messages))
	for index, message := range request.Messages {
		sequences[index] = message.Seq
	}
	return &call{
		draft:     draft,
		sequences: sequences,
		entered:   implementation.entered,
		release:   implementation.release,
	}, nil
}

type call struct {
	draft     session.EventDraft
	sequences []int64
	entered   chan struct{}
	release   <-chan struct{}
}

func (prepared *call) Draft() session.EventDraft { return prepared.draft }
func (prepared *call) Generate(context.Context) (title.ProviderResult, error) {
	if prepared.entered != nil {
		prepared.entered <- struct{}{}
	}
	if prepared.release != nil {
		<-prepared.release
	}
	return title.ProviderResult{
		Title:       "generated",
		MessageSeqs: prepared.sequences,
	}, nil
}
