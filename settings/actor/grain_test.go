package settingsactor

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorenx/goren/mcpclient"
	"github.com/gorenx/goren/settings"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/tools"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/log"
)

type promptFixture struct {
	entered chan struct{}
	release chan struct{}
}

func (fixture *promptFixture) ApplyPrompt(ctx context.Context, _ string, _ systemprompt.Config) error {
	select {
	case fixture.entered <- struct{}{}:
	default:
	}
	select {
	case <-fixture.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type connectionFixture struct{}

func (*connectionFixture) Validate(context.Context, []mcpclient.Config) error { return nil }

func (*connectionFixture) Reconcile(context.Context, []mcpclient.Config) error { return nil }

func TestGrainKeepsQueriesResponsiveAndSerializesConditionalEdits(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	running, err := goakt.NewActorSystem("settings-test", goakt.WithLogger(log.DiscardLogger))
	if err != nil {
		t.Fatal(err)
	}
	if err := running.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer running.Stop(context.Background())
	path := filepath.Join(t.TempDir(), "settings.sqlite")
	installed, err := Open(ctx, path, settings.Values{
		Providers: map[string]settings.ProviderConfig{"deepseek": {}},
		Prompts:   map[string]settings.PromptConfig{"default": {}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer installed.Close(context.Background())
	prompts := &promptFixture{
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	endpoint, err := NewClient(ctx, running)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := endpoint.Describe(ctx); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unbound client did not report unavailable: %v", err)
	}
	_, err = installed.Bind(ctx, running, prompts, &connectionFixture{}, &connectionFixture{})
	if err != nil {
		t.Fatal(err)
	}
	failures := make(chan error, 2)
	go func() {
		persona := "first"
		_, err := endpoint.UpdatePrompt(ctx, "default", 0, settings.PromptConfig{
			Persona: &persona,
		})
		failures <- err
	}()
	select {
	case <-prompts.entered:
	case <-ctx.Done():
		t.Fatal("configuration application never started")
	}
	view, err := endpoint.Describe(ctx)
	if err != nil || view.State.Revision != 0 {
		t.Fatalf("in-flight read did not preserve last completed view: %#v %v", view, err)
	}
	go func() {
		persona := "second"
		_, err := endpoint.UpdatePrompt(ctx, "default", 0, settings.PromptConfig{
			Persona: &persona,
		})
		failures <- err
	}()
	close(prompts.release)
	conflicts := 0
	for range 2 {
		select {
		case err := <-failures:
			if errors.Is(err, settings.ErrConflict) {
				conflicts++
			} else if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal("configuration requests did not settle")
		}
	}
	if conflicts != 1 {
		t.Fatal("concurrent stale editor was not rejected")
	}
	// Client forwards complete replacements and full resets through the same owner.
	disabled := false
	view, err = endpoint.UpdatePrompt(ctx, "default", 1, settings.PromptConfig{
		IncludeHarnessIdentity: &disabled,
	})
	if err != nil || view.State.User.Prompts["default"].Persona != nil {
		t.Fatalf("Actor whole replacement inherited omitted persona: %#v %v", view, err)
	}
	view, err = endpoint.ResetPrompt(ctx, "default", view.State.Version(actorGroup(settings.PromptSection, "default")))
	if err != nil || view.State.User.Has(actorGroup(settings.PromptSection, "default")) {
		t.Fatalf("Actor prompt reset failed: %#v %v", view, err)
	}
	ref := "CUSTOM_API_KEY"
	view, err = endpoint.UpdateProvider(ctx, "deepseek", 0, settings.ProviderConfig{
		APIKeyEnv: &ref,
	})
	if err != nil {
		t.Fatal(err)
	}
	view, err = endpoint.ResetProvider(ctx, "deepseek", view.State.Version(actorGroup(settings.ProviderSection, "deepseek")))
	if err != nil || view.State.User.Has(actorGroup(settings.ProviderSection, "deepseek")) {
		t.Fatalf("Actor provider reset failed: %#v %v", view, err)
	}
	view, err = endpoint.UpdateMCP(ctx, "named", 0, settings.MCPConfig{
		Enabled: true,
		Connection: mcpclient.Config{
			ServerName: "named",
			Transport:  "streamable-http",
			URL:        "https://mcp.example.test",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	view, err = endpoint.ResetMCP(ctx, "named", view.State.Version(actorGroup(settings.MCPSection, "named")))
	if err != nil || len(view.State.User.MCP) != 0 {
		t.Fatalf("Actor MCP reset failed: %#v %v", view, err)
	}
	if err := installed.Close(ctx); err != nil {
		t.Fatal(err)
	}
	resolved, err := Inspect(ctx, path, settings.Values{
		Providers: map[string]settings.ProviderConfig{"deepseek": {}},
		Prompts:   map[string]settings.PromptConfig{"default": {}},
	})
	if err != nil || resolved.Prompts["default"].Persona != nil {
		t.Fatalf("persistent result: %#v %v", resolved, err)
	}
}

func actorGroup(kind settings.Section, name string) settings.Group {
	return settings.Group{
		Section: kind,
		Name:    name,
	}
}

func (*connectionFixture) ValidateTool(context.Context, string) error            { return nil }
func (*connectionFixture) ApplyTool(context.Context, string, tools.Config) error { return nil }

func TestProviderCloseTimeoutRetainsStoreUntilAcceptedEditDrains(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	running, err := goakt.NewActorSystem("settings-close-test", goakt.WithLogger(log.DiscardLogger))
	if err != nil {
		t.Fatal(err)
	}
	if err := running.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer running.Stop(context.Background())
	baseline := settings.Values{
		Prompts: map[string]settings.PromptConfig{"default": {}},
	}
	path := filepath.Join(t.TempDir(), "settings.sqlite")
	installed, err := Open(ctx, path, baseline)
	if err != nil {
		t.Fatal(err)
	}
	defer installed.Close(context.Background())
	prompts := &promptFixture{
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	defer func() {
		select {
		case <-prompts.release:
		default:
			close(prompts.release)
		}
	}()
	endpoint, err := installed.Bind(ctx, running, prompts, &connectionFixture{}, &connectionFixture{})
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() {
		persona := "accepted edit"
		_, err := endpoint.UpdatePrompt(ctx, "default", 0, settings.PromptConfig{
			Persona: &persona,
		})
		finished <- err
	}()
	select {
	case <-prompts.entered:
	case <-ctx.Done():
		t.Fatal("application did not start")
	}
	bounded, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	err = installed.Close(bounded)
	stop()
	if err == nil || installed.storage == nil {
		t.Fatal("timed-out close discarded storage responsibility")
	}
	if _, err := installed.storage.Load(ctx); err != nil {
		t.Fatalf("store closed while an accepted edit was applying: %v", err)
	}
	if _, err := endpoint.UpdatePrompt(ctx, "default", 1, settings.PromptConfig{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("closing provider accepted a new edit: %v", err)
	}
	close(prompts.release)
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("accepted edit did not drain")
	}
	if err := installed.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := installed.Close(ctx); err != nil {
		t.Fatal("repeated close failed", err)
	}
	resolved, err := Inspect(ctx, path, baseline)
	if err != nil ||
		resolved.Prompts["default"].Persona == nil ||
		*resolved.Prompts["default"].Persona != "accepted edit" {
		t.Fatalf("accepted edit did not survive close and reopen: %v", err)
	}
}
