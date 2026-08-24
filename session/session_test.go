package session

import (
	"context"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gorenx/goren/llm"
)

type fixturePayload struct {
	Items []string `json:"items"`
}

type fixedTimeSource struct {
	current time.Time
}

func (source fixedTimeSource) CurrentTime() time.Time {
	return source.current
}

type negativeZeroPayload struct{}

func (negativeZeroPayload) MarshalJSON() ([]byte, error) {
	return []byte("-0"), nil
}

var fixtureEventKey = DefineEvent[fixturePayload]("fixture/event")

func TestCommitSnapshotsPayloadAndEventViews(t *testing.T) {
	t.Parallel()
	fixedTime := time.UnixMilli(1_723_700_000_123)
	conversation, err := newContextWithClock(
		"session-a",
		CreateOptions{},
		fixedTimeSource{
			current: fixedTime,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	body := fixturePayload{Items: []string{"first"}}
	draft, err := NewEventDraft(fixtureEventKey, body)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := conversation.Commit(context.Background(), Batch(draft))
	if err != nil {
		t.Fatal(err)
	}
	committed := receipt.Events[0]
	body.Items[0] = "mutated"
	committed.Data[0] = '['

	detachedEntries := conversation.Events()
	if len(detachedEntries) != 1 || detachedEntries[0].Seq != 0 || detachedEntries[0].Time != fixedTime.UnixMilli() {
		t.Fatalf("events = %#v", detachedEntries)
	}
	var decoded fixturePayload
	if err := json.Unmarshal(detachedEntries[0].Data, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded.Items, []string{"first"}) {
		t.Fatalf("decoded items = %#v", decoded.Items)
	}
	if _, err := commitFixtureEvent(context.Background(), conversation, "second"); err != nil {
		t.Fatal(err)
	}
	if len(detachedEntries) != 1 || conversation.Seq() != 2 {
		t.Fatalf("old detachedEntries length = %d, current seq = %d", len(detachedEntries), conversation.Seq())
	}
}

type blockingPlan struct {
	drafts  []EventDraft
	entered chan struct{}
	resume  chan struct{}
}

func (plan *blockingPlan) Build(
	context.Context,
	Snapshot,
) ([]EventDraft, error) {
	close(plan.entered)
	<-plan.resume
	return plan.drafts, nil
}

func TestCommitKeepsOneBatchAdjacent(t *testing.T) {
	t.Parallel()
	conversation, err := New("atomic-group", CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	firstDraft, err := NewEventDraft(
		fixtureEventKey,
		fixturePayload{
			Items: []string{"group-first"},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	secondDraft, err := NewEventDraft(
		fixtureEventKey,
		fixturePayload{
			Items: []string{"group-second"},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	buildEntered := make(chan struct{})
	releaseBuild := make(chan struct{})
	groupDone := make(chan error, 1)
	go func() {
		_, writeErr := conversation.Commit(
			context.Background(),
			&blockingPlan{
				drafts:  []EventDraft{firstDraft, secondDraft},
				entered: buildEntered,
				resume:  releaseBuild,
			},
		)
		groupDone <- writeErr
	}()
	<-buildEntered

	outsideDone := make(chan error, 1)
	go func() {
		_, writeErr := commitFixtureEvent(context.Background(), conversation, "outside")
		outsideDone <- writeErr
	}()
	close(releaseBuild)
	if groupErr := <-groupDone; groupErr != nil {
		t.Fatal(groupErr)
	}
	if writeErr := <-outsideDone; writeErr != nil {
		t.Fatal(writeErr)
	}

	entries := conversation.Events()
	if len(entries) != 3 {
		t.Fatalf("event count = %d", len(entries))
	}
	wantItems := []string{"group-first", "group-second", "outside"}
	for index, entry := range entries {
		var body fixturePayload
		if decodeErr := json.Unmarshal(entry.Data, &body); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		if !reflect.DeepEqual(body.Items, []string{wantItems[index]}) {
			t.Fatalf("event %d items = %#v", index, body.Items)
		}
	}
}

func TestSeedIsContiguousAndEndsWithLifecycleMarker(t *testing.T) {
	t.Parallel()
	seed := []Event{{Type: "fixture/seed", Seq: 0, Time: 10, Data: json.RawMessage(`{"value":1}`)}}
	conversation, err := New("seeded", CreateOptions{Seed: seed})
	if err != nil {
		t.Fatal(err)
	}
	seed[0].Data[0] = '['
	entries := conversation.Events()
	if conversation.FirstLiveSeq() != 1 || conversation.Seq() != 2 {
		t.Fatalf("first live = %d, seq = %d", conversation.FirstLiveSeq(), conversation.Seq())
	}
	if entries[1].Type != endSeedEventType || string(entries[1].Data) != "{}" {
		t.Fatalf("end-seed entry = %#v", entries[1])
	}
	if string(entries[0].Data) != `{"value":1}` {
		t.Fatalf("seed data = %s", entries[0].Data)
	}

	badSeed := []Event{{Type: "fixture/seed", Seq: 1, Time: 10, Data: json.RawMessage(`{}`)}}
	if _, err := New("bad-seed", CreateOptions{Seed: badSeed}); err == nil || !strings.Contains(err.Error(), "not contiguous") {
		t.Fatalf("non-contiguous seed error = %v", err)
	}
}

func TestSurfaceReplacementIsAtomicAndTracksProvenance(t *testing.T) {
	t.Parallel()
	userKey := defineSurfaceEvent[fixturePayload]("user/message")
	assistantKey := defineSurfaceEvent[fixturePayload]("assistant/message")
	conversation, err := New("surface", CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	{
		draft, err := NewSurfaceEventDraft(userKey, fixturePayload{Items: []string{"u"}}, SurfaceIntent{Operation: SurfaceAppend()})
		if err == nil {
			_, err = conversation.Commit(context.Background(), Batch(draft))
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	sourceZero := []int64{0}
	{
		draft, err := NewSurfaceEventDraft(assistantKey, fixturePayload{Items: []string{"a"}}, SurfaceIntent{
			Operation: SurfaceAppend(), SourceEventSeqs: &sourceZero,
		})
		if err == nil {
			_, err = conversation.Commit(context.Background(), Batch(draft))
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	missing := []int64{1}
	{
		draft, err := NewSurfaceEventDraft(assistantKey, fixturePayload{Items: []string{"summary"}}, SurfaceIntent{
			Operation: SurfaceReplace(0, 1), SourceEventSeqs: &missing,
		})
		if err == nil {
			_, err = conversation.Commit(context.Background(), Batch(draft))
		}
		if err == nil || !strings.Contains(err.Error(), "missing shadowed seq 0") {
			t.Fatalf("missing provenance error = %v", err)
		}
	}
	if conversation.Seq() != 2 || !reflect.DeepEqual(conversation.Surface().Nodes, []int64{0, 1}) {
		t.Fatalf("failed replacement mutated Session: seq=%d surface=%#v", conversation.Seq(), conversation.Surface())
	}
	allSources := []int64{0, 1}
	{
		draft, err := NewSurfaceEventDraft(assistantKey, fixturePayload{Items: []string{"summary"}}, SurfaceIntent{
			Operation: SurfaceReplace(0, 1), SourceEventSeqs: &allSources,
		})
		if err == nil {
			_, err = conversation.Commit(context.Background(), Batch(draft))
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	view := conversation.Surface()
	if !reflect.DeepEqual(view.Nodes, []int64{2}) || view.ReplaceGeneration != 1 {
		t.Fatalf("surface = %#v", view)
	}
}

func TestAppendRejectsNegativeZeroBeforeCommit(t *testing.T) {
	t.Parallel()
	definition := DefineEvent[negativeZeroPayload]("fixture/negative-zero")
	conversation, err := New("json", CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	{
		draft, err := NewEventDraft(definition, negativeZeroPayload{})
		if err == nil {
			_, err = conversation.Commit(context.Background(), Batch(draft))
		}
		if err == nil || !strings.Contains(err.Error(), "invalid JSON number") {
			t.Fatalf("negative zero error = %v", err)
		}
	}
	if conversation.Seq() != 0 {
		t.Fatalf("seq = %d after rejected append", conversation.Seq())
	}
	if !math.Signbit(math.Copysign(0, -1)) {
		t.Fatal("test fixture did not construct negative zero")
	}
}

func TestRequestFoldsAndDerivedMessagesTrackCurrentSurface(t *testing.T) {
	t.Parallel()
	conversation, err := New("agent-session", CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	maximum := 512
	prompt := "system"
	requestSnapshot := EpochHeader{
		Config: llm.CallConfig{Provider: "mock", Model: "m", MaxTokens: &maximum, Stop: []string{"done"}},
		System: &prompt,
		Tools:  []llm.ToolSchema{{Name: "echo", Description: "echo", Parameters: json.RawMessage(`{"type":"object"}`)}},
	}
	{
		draft, err := NewEventDraft(RequestHeaderSet, RequestHeaderSnapshot{
			Header: requestSnapshot, Reason: RequestHeaderInitial,
		})
		if err == nil {
			_, err = conversation.Commit(context.Background(), Batch(draft))
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	window := 4096
	{
		draft, err := NewEventDraft(RequestContextSet, RequestRouteContext{
			Provider: "mock", Model: "m", ContextWindow: &window,
		})
		if err == nil {
			_, err = conversation.Commit(context.Background(), Batch(draft))
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	requestSnapshot.Config.Stop[0] = "mutated"
	requestSnapshot.Tools[0].Parameters[0] = '['

	foldedHeader, err := LatestRequestHeader(conversation.Events())
	if err != nil || foldedHeader == nil {
		t.Fatalf("request header = %#v, error = %v", foldedHeader, err)
	}
	if foldedHeader.Config.Stop[0] != "done" || string(foldedHeader.Tools[0].Parameters) != `{"type":"object"}` {
		t.Fatalf("request header aliases input = %#v", foldedHeader)
	}
	foldedContext, err := LatestRequestContext(conversation.Events())
	if err != nil || foldedContext == nil || foldedContext.ContextWindow == nil || *foldedContext.ContextWindow != 4096 {
		t.Fatalf("request context = %#v, error = %v", foldedContext, err)
	}

	userInput, err := llm.NewUserMessage(llm.UserMessageInput{
		Content: []llm.ContentBlock{llm.NewTextBlock("hello")}, Source: llm.UserMessageSource{},
	})
	if err != nil {
		t.Fatal(err)
	}
	{
		draft, err := NewSurfaceEventDraft(UserMessageAdded, userInput, SurfaceIntent{Operation: SurfaceAppend()})
		if err == nil {
			_, err = conversation.Commit(context.Background(), Batch(draft))
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	emptyReply, err := llm.NewAssistantMessage(llm.AssistantMessageInput{
		Source: llm.ModelMessageSource{Provider: "mock", Model: "m"},
	})
	if err != nil {
		t.Fatal(err)
	}
	{
		draft, err := NewSurfaceEventDraft(AssistantMessaged, AssistantMessage{
			Turn: 1, Step: 1, Message: emptyReply,
		}, SurfaceIntent{Operation: SurfaceAppend()})
		if err == nil {
			_, err = conversation.Commit(context.Background(), Batch(draft))
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	assistantReply, err := llm.NewAssistantMessage(llm.AssistantMessageInput{
		Content: []llm.ContentBlock{llm.NewTextBlock("working")},
		Source:  llm.ModelMessageSource{Provider: "mock", Model: "m"},
	})
	if err != nil {
		t.Fatal(err)
	}
	{
		draft, err := NewSurfaceEventDraft(AssistantMessaged, AssistantMessage{
			Turn: 1, Step: 1, Message: assistantReply,
		}, SurfaceIntent{Operation: SurfaceAppend()})
		if err == nil {
			_, err = conversation.Commit(context.Background(), Batch(draft))
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	toolReply, err := llm.NewToolResultMessage(llm.ToolResultMessageInput{
		CallID: "call-1", Content: []llm.ContentBlock{llm.NewTextBlock("result")},
	})
	if err != nil {
		t.Fatal(err)
	}
	{
		draft, err := NewSurfaceEventDraft(ToolResultAdded, ToolResult{
			Turn: 1, Step: 1, Message: toolReply,
		}, SurfaceIntent{Operation: SurfaceAppend()})
		if err == nil {
			_, err = conversation.Commit(context.Background(), Batch(draft))
		}
		if err != nil {
			t.Fatal(err)
		}
	}

	derived, err := conversation.DeriveMessages()
	if err != nil {
		t.Fatal(err)
	}
	if len(derived) != 3 || derived[0].StableID() != userInput.StableID() ||
		derived[1].StableID() != assistantReply.StableID() || derived[2].StableID() != toolReply.StableID() {
		t.Fatalf("derived messages = %#v", derived)
	}
	replacement, err := llm.NewUserMessage(llm.UserMessageInput{
		Content: []llm.ContentBlock{llm.NewTextBlock("summary")},
		Source:  llm.PluginMessageSource{Plugin: "compactor"},
	})
	if err != nil {
		t.Fatal(err)
	}
	provenance := []int64{2, 3, 4, 5}
	{
		draft, err := NewSurfaceEventDraft(UserMessageAdded, replacement, SurfaceIntent{
			Operation: SurfaceReplace(2, 5), SourceEventSeqs: &provenance,
		})
		if err == nil {
			_, err = conversation.Commit(context.Background(), Batch(draft))
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	derived, err = conversation.DeriveMessages()
	if err != nil {
		t.Fatal(err)
	}
	if len(derived) != 1 || derived[0].StableID() != replacement.StableID() {
		t.Fatalf("derived messages after replacement = %#v", derived)
	}
}
