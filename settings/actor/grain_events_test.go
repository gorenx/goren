package settingsactor

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorenx/goren/settings"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
)

type settingsObserver struct {
	ready   chan struct{}
	notices chan ChangeNotice
}

func (*settingsObserver) PreStart(*goakt.Context) error { return nil }
func (*settingsObserver) PostStop(*goakt.Context) error { return nil }
func (sink *settingsObserver) Receive(rc *goakt.ReceiveContext) {
	switch incoming := rc.Message().(type) {
	case *goakt.PostStart:
		rc.Tell(rc.ActorSystem().TopicActor(), goakt.NewSubscribe(ChangedTopic))
	case *goakt.SubscribeAck:
		close(sink.ready)
	case ChangeNotice:
		sink.notices <- incoming
	}
}

func TestSavedSettingsPublishDetachedGlobalSnapshots(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	running, err := goakt.NewActorSystem("settings-notices", goakt.WithPubSub(), goakt.WithLogger(log.DiscardLogger))
	if err != nil {
		t.Fatal(err)
	}
	if err := running.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = running.Stop(context.Background()) })
	baseline := settings.Directory{
		Values: settings.Values{
			Prompts: map[string]settings.PromptConfig{
				"default": {},
			},
		},
	}
	path := filepath.Join(t.TempDir(), "settings.sqlite")
	installed, err := Open(ctx, path, baseline)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = installed.Close(context.Background()) })
	endpoint, err := installed.Bind(ctx, running, &connectionFixture{}, &connectionFixture{})
	if err != nil {
		t.Fatal(err)
	}
	sink := &settingsObserver{
		ready:   make(chan struct{}),
		notices: make(chan ChangeNotice, 16),
	}
	if _, err := running.Spawn(ctx, "settings-observer", sink); err != nil {
		t.Fatal(err)
	}
	select {
	case <-sink.ready:
	case <-ctx.Done():
		t.Fatal("subscription timed out")
	}
	initial, err := endpoint.Describe(ctx)
	if err != nil || initial.State.Revision != 0 {
		t.Fatalf("initial configuration: %#v, %v", initial, err)
	}
	receive := func(expected int64) ChangeNotice {
		t.Helper()
		select {
		case notice := <-sink.notices:
			if notice.Revision != expected {
				t.Fatalf("notice revision = %d, want %d", notice.Revision, expected)
			}
			stored, err := installed.storage.Load(ctx)
			if err != nil || stored.Revision != expected {
				t.Fatalf("notice preceded durable save: %#v, %v", stored, err)
			}
			return notice
		case <-ctx.Done():
			t.Fatal("configuration notice timed out")
			return ChangeNotice{}
		}
	}
	persona := "first"
	first, err := endpoint.UpdatePrompt(ctx, "default", 0, settings.PromptConfig{
		Persona: &persona,
	})
	if err != nil || first.State.Revision != 1 {
		t.Fatalf("saved snapshot: %#v, %v", first, err)
	}
	notice := receive(1)
	// A stale edit and a no-op cannot invent another revision.
	if _, err := endpoint.UpdatePrompt(ctx, "default", 0, settings.PromptConfig{}); !errors.Is(err, settings.ErrConflict) {
		t.Fatal("stale edit was accepted", err)
	}
	if _, err := endpoint.UpdatePrompt(ctx, "default", 1, settings.PromptConfig{
		Persona: &persona,
	}); err != nil {
		t.Fatal(err)
	}
	// An unrelated group advances the same global counter, preserving old values.
	if _, err := endpoint.UpdateAgent(ctx, "reviewer", 0, settings.Values{}); err != nil {
		t.Fatal(err)
	}
	second := receive(2)
	if _, found := second.Configuration().Agents["reviewer"]; !found {
		t.Fatal("notice omitted another group's configuration")
	}
	detached := notice.Configuration()
	*detached.Prompts["default"].Persona = "mutated by a subscriber"
	if *notice.Configuration().Prompts["default"].Persona != "first" || len(notice.Configuration().Agents) != 0 {
		t.Fatal("saved notice changed through a consumer or later save")
	}
	reset, err := endpoint.ResetPrompt(ctx, "default", 1)
	if err != nil || reset.State.Revision != 3 {
		t.Fatalf("reset: %#v, %v", reset, err)
	}
	if receive(3).Configuration().Prompts["default"].Persona != nil {
		t.Fatal("reset notice retained removed override")
	}
	if err := installed.Close(ctx); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path, baseline)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close(context.Background()) })
	if reopened.stored.Revision != 3 || reopened.Target().Prompts["default"].Persona != nil {
		t.Fatal("restart did not recover the latest complete configuration")
	}
	if _, found := reopened.Target().Agents["reviewer"]; !found {
		t.Fatal("restart lost an unchanged group")
	}
}
