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

var titleFixtureConfig = Config{
	FallbackMaxWords: 5,
	FallbackMaxBytes: 40,
	MaxTitleBytes:    80,
}

type titleFailureReporter struct{}

func (titleFailureReporter) ReportEventFailure(context.Context, plugin.EventFailure) {}

func (titleFailureReporter) ReportPostCommitFailure(session.PostCommitFailure) {}

func (titleFailureReporter) ReportAsyncFailure(AsyncFailure) {}

type titleProjectionObserver struct {
	plugin.Base
	changes chan sessionprojection.Change
}

func (*titleProjectionObserver) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "fixture-session-title-projection-observer",
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[sessionprojection.Registry](),
		},
		Events: []plugin.EventSubscription{
			plugin.EventOf[sessionprojection.ProjectionChanged](),
		},
	}
}

func (observer *titleProjectionObserver) Apply(context.Context) error {
	_, err := plugin.Require[sessionprojection.Registry](observer)
	return err
}

func (*titleProjectionObserver) Dispose(context.Context) error {
	return nil
}

func (observer *titleProjectionObserver) ObserveEvent(
	_ context.Context,
	fact plugin.Event,
) error {
	projectionChange, matches := fact.(sessionprojection.ProjectionChanged)
	if matches && projectionChange.Change.Key == ProjectionKey {
		observer.changes <- projectionChange.Change
	}
	return nil
}

type titleFixture struct {
	store       *session.MemoryStore
	titles      *LogService
	projections *sessionprojection.DriveRegistry
	changes     <-chan sessionprojection.Change
}

func newTitleFixture(testingContext *testing.T) titleFixture {
	testingContext.Helper()
	reporter := titleFailureReporter{}
	store, err := session.NewMemoryStore(
		session.MemoryStoreOptions{
			PostCommitFailures: reporter,
		},
	)
	if err != nil {
		testingContext.Fatal(err)
	}
	projections := sessionprojection.NewDriveRegistry()
	titles, err := NewLogService(titleFixtureConfig, reporter)
	if err != nil {
		testingContext.Fatal(err)
	}
	changes := make(chan sessionprojection.Change, 8)
	observer := &titleProjectionObserver{
		changes: changes,
	}
	runtimeEngine := plugin.NewRuntime(
		plugin.RuntimeSettings{
			EventFailures: reporter,
		},
	)
	if _, err := runtimeEngine.Start(
		context.Background(),
		observer,
		titles,
		projections,
		store,
	); err != nil {
		testingContext.Fatal(err)
	}
	testingContext.Cleanup(func() {
		if err := runtimeEngine.Shutdown(context.Background()); err != nil {
			testingContext.Error(err)
		}
	})
	return titleFixture{
		store:       store,
		titles:      titles,
		projections: projections,
		changes:     changes,
	}
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
	wireValue, err := json.Marshal(
		EventData{
			Title:       "Named",
			MessageSeqs: []int64{},
			Source: UserSource{
				Kind: "user",
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(wireValue, []byte(`"messageSeqs":[]`)) {
		t.Fatalf("user title event = %s, want an empty JSON array", wireValue)
	}
	var decoded EventData
	if err := json.Unmarshal(
		[]byte(`{"title":"Named","messageSeqs":null,"source":{"kind":"user"}}`),
		&decoded,
	); err == nil {
		t.Fatal("null messageSeqs was accepted")
	}
}

func TestLogServiceFallbackRenameAndProjection(t *testing.T) {
	t.Parallel()
	requestContext := context.Background()
	fixture := newTitleFixture(t)
	handle, err := fixture.store.Create(requestContext, nil, session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	conversation := handle.Session()
	initial, err := fixture.projections.Snapshot(conversation)
	if err != nil || string(initial.Values[ProjectionKey]) != "null" {
		t.Fatalf("initial title projection = (%#v, %v)", initial, err)
	}
	promptSeq := appendTitleFixtureHuman(t, conversation, "Original prompt text")
	fallbackChange := receiveTitleChange(t, fixture.changes)
	if string(fallbackChange.Value) != `"Original prompt text"` {
		t.Fatalf("fallback projection = %s", fallbackChange.Value)
	}
	fallback, err := fixture.titles.Get(conversation)
	if err != nil {
		t.Fatal(err)
	}
	if fallback == nil || fallback.Source.SourceKind() != "fallback" ||
		len(fallback.MessageSeqs) != 1 || fallback.MessageSeqs[0] != promptSeq {
		t.Fatalf("fallback snapshot = %#v", fallback)
	}

	accepted, err := fixture.titles.Rename(
		requestContext,
		conversation,
		"  Hand\tpicked   name  ",
	)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Title != "Hand picked name" || accepted.Source.SourceKind() != "user" ||
		len(accepted.MessageSeqs) != 0 {
		t.Fatalf("renamed snapshot = %#v", accepted)
	}
	renameChange := receiveTitleChange(t, fixture.changes)
	if renameChange.Seq != accepted.EventSeq ||
		string(renameChange.Value) != `"Hand picked name"` {
		t.Fatalf("rename projection = %#v", renameChange)
	}
	if _, err := fixture.titles.Rename(
		requestContext,
		conversation,
		" \x1b[31m ",
	); err == nil {
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
	fixture := newTitleFixture(t)
	started := make(chan ProviderRequest, 1)
	aborted := make(chan struct{}, 1)
	implementation := titleFixtureProvider{
		identifier: "fixture-provider",
		mode:       AutomaticAllPrompts,
		generate: func(
			callContext context.Context,
			request ProviderRequest,
		) (ProviderResult, error) {
			started <- request
			<-callContext.Done()
			aborted <- struct{}{}
			return ProviderResult{}, callContext.Err()
		},
	}
	if _, err := fixture.titles.Register(implementation); err != nil {
		t.Fatal(err)
	}
	handle, err := fixture.store.Create(requestContext, nil, session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	conversation := handle.Session()
	promptSeq := appendTitleFixtureHuman(t, conversation, "Prompt that triggers generation")
	{
		draft, err := session.NewEventDraft(session.RequestHeaderSet,
			session.RequestHeaderSnapshot{
				Header: session.EpochHeader{
					Config: llm.CallConfig{
						Provider: "main-route",
						Model:    "chat-model",
					},
				},
				Reason: session.RequestHeaderChange,
			})
		if err == nil {
			_, err = conversation.Commit(context.Background(), session.Batch(draft))
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	select {
	case request := <-started:
		if len(request.Messages) != 1 || request.Messages[0].Seq != promptSeq ||
			request.Route == nil || request.Route.Provider != "main-route" ||
			request.Route.Model != "chat-model" {
			t.Fatalf("provider request = %#v", request)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("title provider did not start")
	}
	accepted, err := fixture.titles.Rename(
		requestContext,
		conversation,
		"User wins",
	)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-aborted:
	case <-time.After(2 * time.Second):
		t.Fatal("active title provider was not canceled")
	}
	latest, err := fixture.titles.Get(conversation)
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
	fixture := newTitleFixture(t)
	handle, err := fixture.store.Create(requestContext, nil, session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	conversation := handle.Session()
	appendTitleFixtureHuman(t, conversation, "Derivable prompt words")
	receiveTitleChange(t, fixture.changes)
	if _, err := fixture.titles.Rename(
		requestContext,
		conversation,
		"Pinned without provider",
	); err != nil {
		t.Fatal(err)
	}
	receiveTitleChange(t, fixture.changes)
	refreshed, err := fixture.titles.Refresh(requestContext, conversation)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed == nil || refreshed.Title != "Derivable prompt words" ||
		refreshed.Source.SourceKind() != "fallback" {
		t.Fatalf("refreshed title = %#v", refreshed)
	}
}

type titleFixtureProvider struct {
	identifier ProviderID
	mode       AutomaticMode
	generate   func(context.Context, ProviderRequest) (ProviderResult, error)
}

func (implementation titleFixtureProvider) ID() ProviderID {
	return implementation.identifier
}

func (implementation titleFixtureProvider) AutomaticMode() AutomaticMode {
	return implementation.mode
}

func (implementation titleFixtureProvider) Generate(
	requestContext context.Context,
	request ProviderRequest,
) (ProviderResult, error) {
	return implementation.generate(requestContext, request)
}

func appendTitleFixtureHuman(
	testingContext *testing.T,
	conversation session.Context,
	text string,
) int64 {
	testingContext.Helper()
	messageValue, err := llm.NewUserMessage(
		llm.UserMessageInput{
			Content: []llm.ContentBlock{
				llm.NewTextBlock(text),
			},
			Source: llm.UserMessageSource{
				Kind: "user",
			},
		},
	)
	if err != nil {
		testingContext.Fatal(err)
	}
	committed, err := session.Event{}, error(nil)
	{
		var committedEvent session.Event
		var writeErr error
		draft, draftErr := session.NewSurfaceEventDraft(session.UserMessageAdded,
			messageValue,
			session.SurfaceIntent{
				Operation: session.SurfaceAppend(),
			})
		writeErr = draftErr
		if draftErr == nil {
			receipt, commitErr := conversation.Commit(context.Background(), session.Batch(draft))
			writeErr = commitErr
			if commitErr == nil {
				committedEvent = receipt.Events[0]
			}
		}
		committed = committedEvent
		err = writeErr
	}

	if err != nil {
		testingContext.Fatal(err)
	}
	return committed.Seq
}

func receiveTitleChange(
	testingContext *testing.T,
	changes <-chan sessionprojection.Change,
) sessionprojection.Change {
	testingContext.Helper()
	select {
	case projectionChange := <-changes:
		var titleValue string
		if err := json.Unmarshal(projectionChange.Value, &titleValue); err != nil {
			testingContext.Fatalf(
				"title projection JSON = %s: %v",
				projectionChange.Value,
				err,
			)
		}
		return projectionChange
	case <-time.After(2 * time.Second):
		testingContext.Fatal("title projection change did not arrive")
		return sessionprojection.Change{}
	}
}
