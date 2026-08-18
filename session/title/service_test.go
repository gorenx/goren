package title

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	sessionprojection "github.com/gorenx/goren/session/projection"
)

var titleFixtureConfig = Config{FallbackMaxWords: 5, FallbackMaxBytes: 40, MaxTitleBytes: 80}

type titleFixturePlugin struct {
	titles      *LogService
	projections *sessionprojection.DriveRegistry
	store       *session.MemoryStore
	scope       *plugin.Scope
}

func (*titleFixturePlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "fixture-session-title",
		Provides: []plugin.ServiceRef{
			Service.Ref(), sessionprojection.Service.Ref(), session.StoreService.Ref(),
		},
	}
}

func (instance *titleFixturePlugin) Apply(requestContext context.Context, pluginScope *plugin.Scope) error {
	projections, err := sessionprojection.NewDriveRegistry(pluginScope)
	if err != nil {
		return err
	}
	storeProvider, err := session.NewMemoryStore(pluginScope, session.MemoryStoreOptions{})
	if err != nil {
		return err
	}
	if err := pluginScope.Effect(requestContext, "sessions", func(context.Context) (plugin.Disposer, error) {
		return storeProvider.Close, nil
	}); err != nil {
		return err
	}
	titles, err := NewLogService(pluginScope, storeProvider, projections, titleFixtureConfig, Options{})
	if err != nil {
		return err
	}
	instance.titles = titles
	instance.projections = projections
	instance.store = storeProvider
	instance.scope = pluginScope
	if _, err := plugin.Provide(pluginScope, Service, TitleService(titles)); err != nil {
		return err
	}
	if _, err := plugin.Provide(pluginScope, sessionprojection.Service, sessionprojection.Registry(projections)); err != nil {
		return err
	}
	_, err = plugin.Provide(pluginScope, session.StoreService, session.LiveStore(storeProvider))
	return err
}

type titleFixtureProvider struct {
	identifier ProviderID
	mode       AutomaticMode
	generate   func(context.Context, ProviderRequest) (ProviderResult, error)
}

func (implementation titleFixtureProvider) ID() ProviderID { return implementation.identifier }

func (implementation titleFixtureProvider) AutomaticMode() AutomaticMode { return implementation.mode }

func (implementation titleFixtureProvider) Generate(
	requestContext context.Context,
	request ProviderRequest,
) (ProviderResult, error) {
	return implementation.generate(requestContext, request)
}

func TestNormalizeAndFallbackSessionTitle(t *testing.T) {
	t.Parallel()
	normalized, err := NormalizeSessionTitle("\x1b]0;hidden\x07  Hello\t world \u202e", 80)
	if err != nil {
		t.Fatal(err)
	}
	if normalized != "Hello world" {
		t.Fatalf("normalized title = %q", normalized)
	}
	fallback, err := FallbackSessionTitle("one two three four", 3, 9)
	if err != nil {
		t.Fatal(err)
	}
	if fallback != "one two t" {
		t.Fatalf("fallback title = %q", fallback)
	}
	bounded, err := TruncateTitleUTF8("你好世界", 7)
	if err != nil {
		t.Fatal(err)
	}
	if bounded != "你好" {
		t.Fatalf("UTF-8 bounded title = %q", bounded)
	}
}

func TestEventDataKeepsUserMessageSeqsAsJSONArray(t *testing.T) {
	t.Parallel()
	wireValue, err := json.Marshal(EventData{
		Title: "Named", MessageSeqs: []int64{}, Source: UserSource{Kind: "user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(wireValue, []byte(`"messageSeqs":[]`)) {
		t.Fatalf("user title event = %s, want an empty JSON array", wireValue)
	}
	var decoded EventData
	if err := json.Unmarshal([]byte(`{"title":"Named","messageSeqs":null,"source":{"kind":"user"}}`), &decoded); err == nil {
		t.Fatal("null messageSeqs was accepted")
	}
}

func TestLogServiceFallbackRenameAndProjection(t *testing.T) {
	t.Parallel()
	requestContext := context.Background()
	engine := plugin.NewRuntime()
	host := &titleFixturePlugin{}
	providerHandle, err := engine.Load(requestContext, host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Unload(requestContext, providerHandle) })
	conversation, err := host.store.Create(requestContext, host.scope, nil, session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := host.projections.Snapshot(conversation)
	if err != nil || string(initial.Values[ProjectionKey]) != "null" {
		t.Fatalf("initial title projection = (%#v, %v)", initial, err)
	}
	changes := make(chan sessionprojection.Change, 4)
	if _, err := host.projections.OnChanged(host.scope, sessionprojection.ChangeListenerFunc(func(projectionChange sessionprojection.Change) {
		if projectionChange.Key == ProjectionKey {
			changes <- projectionChange
		}
	})); err != nil {
		t.Fatal(err)
	}
	promptSeq := appendTitleFixtureHuman(t, conversation, "Original prompt text")
	fallbackChange := receiveTitleChange(t, changes)
	if string(fallbackChange.Value) != `"Original prompt text"` {
		t.Fatalf("fallback projection = %s", fallbackChange.Value)
	}
	fallback, err := host.titles.Get(conversation)
	if err != nil {
		t.Fatal(err)
	}
	if fallback == nil || fallback.Source.SourceKind() != "fallback" ||
		len(fallback.MessageSeqs) != 1 || fallback.MessageSeqs[0] != promptSeq {
		t.Fatalf("fallback snapshot = %#v", fallback)
	}

	accepted, err := host.titles.Rename(conversation, "  Hand\tpicked   name  ")
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Title != "Hand picked name" || accepted.Source.SourceKind() != "user" || len(accepted.MessageSeqs) != 0 {
		t.Fatalf("renamed snapshot = %#v", accepted)
	}
	renameChange := receiveTitleChange(t, changes)
	if renameChange.Seq != accepted.EventSeq || string(renameChange.Value) != `"Hand picked name"` {
		t.Fatalf("rename projection = %#v", renameChange)
	}
	if _, err := host.titles.Rename(conversation, " \x1b[31m "); err == nil {
		t.Fatal("control-only rename succeeded")
	} else {
		var invalid *SessionTitleInvalidError
		if !errors.As(err, &invalid) {
			t.Fatalf("rename error = %T %v", err, err)
		}
	}
}

func TestLogServiceRenameSupersedesActiveProvider(t *testing.T) {
	t.Parallel()
	requestContext := context.Background()
	engine := plugin.NewRuntime()
	host := &titleFixturePlugin{}
	providerHandle, err := engine.Load(requestContext, host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Unload(requestContext, providerHandle) })
	started := make(chan ProviderRequest, 1)
	aborted := make(chan struct{}, 1)
	implementation := titleFixtureProvider{
		identifier: "fixture-provider", mode: AutomaticAllPrompts,
		generate: func(callContext context.Context, request ProviderRequest) (ProviderResult, error) {
			started <- request
			<-callContext.Done()
			aborted <- struct{}{}
			return ProviderResult{}, callContext.Err()
		},
	}
	if _, err := host.titles.Register(host.scope, implementation); err != nil {
		t.Fatal(err)
	}
	conversation, err := host.store.Create(requestContext, host.scope, nil, session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	promptSeq := appendTitleFixtureHuman(t, conversation, "Prompt that triggers generation")
	if _, err := session.Append(conversation, session.RequestHeaderSet, session.RequestHeaderSnapshot{
		Header: session.EpochHeader{Config: llm.CallConfig{Provider: "main-route", Model: "chat-model"}},
		Reason: session.RequestHeaderChange,
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case request := <-started:
		if len(request.Messages) != 1 || request.Messages[0].Seq != promptSeq || request.Route == nil ||
			request.Route.Provider != "main-route" || request.Route.Model != "chat-model" {
			t.Fatalf("provider request = %#v", request)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("title provider did not start")
	}
	accepted, err := host.titles.Rename(conversation, "User wins")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-aborted:
	case <-time.After(2 * time.Second):
		t.Fatal("active title provider was not canceled")
	}
	latest, err := host.titles.Get(conversation)
	if err != nil {
		t.Fatal(err)
	}
	if latest == nil || latest.Title != accepted.Title || latest.Source.SourceKind() != "user" {
		t.Fatalf("latest title = %#v", latest)
	}
}

func TestLogServiceFallbackRefreshUnpinsUserTitle(t *testing.T) {
	t.Parallel()
	requestContext := context.Background()
	engine := plugin.NewRuntime()
	host := &titleFixturePlugin{}
	providerHandle, err := engine.Load(requestContext, host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Unload(requestContext, providerHandle) })
	conversation, err := host.store.Create(requestContext, host.scope, nil, session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	changes := make(chan sessionprojection.Change, 4)
	if _, err := host.projections.OnChanged(host.scope, sessionprojection.ChangeListenerFunc(func(projectionChange sessionprojection.Change) {
		if projectionChange.Key == ProjectionKey {
			changes <- projectionChange
		}
	})); err != nil {
		t.Fatal(err)
	}
	appendTitleFixtureHuman(t, conversation, "Derivable prompt words")
	receiveTitleChange(t, changes)
	if _, err := host.titles.Rename(conversation, "Pinned without provider"); err != nil {
		t.Fatal(err)
	}
	receiveTitleChange(t, changes)
	refreshed, err := host.titles.Refresh(requestContext, conversation)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed == nil || refreshed.Title != "Derivable prompt words" || refreshed.Source.SourceKind() != "fallback" {
		t.Fatalf("refreshed title = %#v", refreshed)
	}
}

func appendTitleFixtureHuman(t *testing.T, conversation *session.Session, text string) int64 {
	t.Helper()
	messageValue, err := llm.NewUserMessage(llm.UserMessageInput{
		Content: []llm.ContentBlock{llm.NewTextBlock(text)}, Source: llm.UserMessageSource{Kind: "user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	committed, err := session.AppendSurface(conversation, session.UserMessageAdded, messageValue, session.SurfaceIntent{
		Operation: session.SurfaceAppend(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return committed.Seq
}

func receiveTitleChange(t *testing.T, changes <-chan sessionprojection.Change) sessionprojection.Change {
	t.Helper()
	select {
	case projectionChange := <-changes:
		var titleValue string
		if err := json.Unmarshal(projectionChange.Value, &titleValue); err != nil {
			t.Fatalf("title projection JSON = %s: %v", projectionChange.Value, err)
		}
		return projectionChange
	case <-time.After(2 * time.Second):
		t.Fatal("title projection change did not arrive")
		return sessionprojection.Change{}
	}
}
