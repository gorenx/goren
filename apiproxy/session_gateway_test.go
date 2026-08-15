package apiproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

func TestSessionRequestDecodersMatchIncludedUnionShapes(t *testing.T) {
	t.Parallel()
	decodedPrompt, issues := DecodeSessionPromptRequest(json.RawMessage(`{
		"sessionId":"session-1","mode":"queue",
		"content":[{"type":"text","text":"hello","ignored":true}],
		"clientTimeZone":"UTC","ignored":true
	}`))
	if len(issues) != 0 {
		t.Fatalf("prompt issues = %#v", issues)
	}
	if decodedPrompt.SessionID != "session-1" || decodedPrompt.Mode != "queue" || decodedPrompt.ClientTimeZone == nil ||
		*decodedPrompt.ClientTimeZone != "UTC" || !reflect.DeepEqual(decodedPrompt.Content, []PromptContentPart{PromptTextPart{Text: "hello"}}) {
		t.Fatalf("prompt = %#v", decodedPrompt)
	}
	update, issues := DecodeSessionUpdateQueueRequest(json.RawMessage(`{
		"sessionId":"session-1","itemId":"message-1",
		"action":{"kind":"edit","content":[{"type":"text","text":"changed","extension":1}]}
	}`))
	if len(issues) != 0 {
		t.Fatalf("update issues = %#v", issues)
	}
	edit, matched := update.Action.(EditQueueAction)
	if !matched || len(edit.Content) != 1 || string(edit.Content[0]) != `{"type":"text","text":"changed","extension":1}` {
		t.Fatalf("update = %#v", update)
	}
}

func TestSessionRequestDecodersRejectNullAndInvalidCombinations(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name   string
		decode func(json.RawMessage) int
		input  string
	}{
		{
			name: "create null id",
			decode: func(rawValue json.RawMessage) int {
				_, issues := DecodeSessionCreateRequest(rawValue)
				return len(issues)
			},
			input: `{"sessionId":null}`,
		},
		{
			name: "create workspace and cwd",
			decode: func(rawValue json.RawMessage) int {
				_, issues := DecodeSessionCreateRequest(rawValue)
				return len(issues)
			},
			input: `{"workspaceId":"workspace-1","cwd":"/tmp"}`,
		},
		{
			name: "history zero messages",
			decode: func(rawValue json.RawMessage) int {
				_, issues := DecodeSessionHistoryRequest(rawValue)
				return len(issues)
			},
			input: `{"sessionId":"session-1","maxMessages":0}`,
		},
		{
			name: "edit null content",
			decode: func(rawValue json.RawMessage) int {
				_, issues := DecodeSessionUpdateQueueRequest(rawValue)
				return len(issues)
			},
			input: `{"sessionId":"session-1","itemId":"message-1","action":{"kind":"edit","content":null}}`,
		},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if count := testCase.decode(json.RawMessage(testCase.input)); count == 0 {
				t.Fatal("invalid request was accepted")
			}
		})
	}
}

func TestHistoryPageCutsAtAppendMessageGroup(t *testing.T) {
	t.Parallel()
	appendOperation := session.SurfaceAppend()
	sources := []int64{3, 4}
	events := []session.Event{
		{Type: "turn/start", Seq: 0, Time: 1, Data: json.RawMessage(`{}`)},
		{Type: session.UserMessageEventName, Seq: 1, Time: 2, Data: json.RawMessage(`{}`), SurfaceOp: &appendOperation},
		{Type: "assistant/chunk", Seq: 2, Time: 3, Data: json.RawMessage(`{}`)},
		{Type: "assistant/chunk", Seq: 3, Time: 4, Data: json.RawMessage(`{}`)},
		{Type: session.AssistantMessageEventName, Seq: 5, Time: 6, Data: json.RawMessage(`{}`), SourceEventSeqs: &sources, SurfaceOp: &appendOperation},
		{Type: "turn/end", Seq: 6, Time: 7, Data: json.RawMessage(`{}`)},
	}
	page, hasMore, err := historyPage(events, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !hasMore || len(page) != 3 || page[0].Event.Seq != 3 || page[2].Event.Seq != 6 {
		t.Fatalf("page = %#v, hasMore = %t", page, hasMore)
	}
}

func TestProjectQueueFoldsDurableSplicesAndUserPlacement(t *testing.T) {
	t.Parallel()
	origin, err := llm.NewOpaqueMessageSource("user", json.RawMessage(`{"kind":"user","rpcId":"rpc-1"}`))
	if err != nil {
		t.Fatal(err)
	}
	messageValue, err := llm.NewUserMessage(llm.UserMessageInput{
		Content: []llm.ContentBlock{llm.NewTextBlock("queued")}, Source: origin,
	})
	if err != nil {
		t.Fatal(err)
	}
	mutation := agent.InboxSplice{Target: agent.NextStep, Start: 0, Inserted: []llm.UserMessage{messageValue}}
	encoded, err := json.Marshal(mutation)
	if err != nil {
		t.Fatal(err)
	}
	items, err := projectQueue(session.Header{}, []session.Event{{
		Type: "agent/inbox/spliced", Seq: 0, Time: 1, Data: encoded,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Placement != QueueSteering || items[0].ID != MessageID(messageValue.StableID()) {
		t.Fatalf("items = %#v", items)
	}
}

func TestReplaceMessageContentPreservesLooseTextExtension(t *testing.T) {
	t.Parallel()
	messageValue, err := llm.NewUserMessage(llm.UserMessageInput{
		Content: []llm.ContentBlock{llm.NewTextBlock("before")}, Source: llm.UserMessageSource{},
	})
	if err != nil {
		t.Fatal(err)
	}
	replaced, err := replaceMessageContent(messageValue, []json.RawMessage{
		json.RawMessage(`{"type":"text","text":"after","extension":1}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(replaced)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(encoded) || !reflect.DeepEqual(replaced.SourceValue(), messageValue.SourceValue()) ||
		!bytes.Contains(encoded, []byte(`"extension":1`)) {
		t.Fatalf("replaced message = %s", encoded)
	}
	readable, supported := replaced.ContentValue()[0].(llm.PlainTextContent)
	if !supported {
		t.Fatalf("extended text type = %T, want PlainTextContent", replaced.ContentValue()[0])
	}
	text, available := readable.PlainText()
	if !available || text != "after" {
		t.Fatalf("extended text = %q, available = %t", text, available)
	}
}

func TestMuxBaselineHighwaterSuppressesLateCommittedCallback(t *testing.T) {
	conversation, err := session.New("session-1", session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	committed, err := session.Append(conversation, session.TurnStarted, session.TurnStart{Turn: 1})
	if err != nil {
		t.Fatal(err)
	}
	nextRPC := 0
	hub := newSessionFrameHub(func() (connection.RPCID, error) {
		nextRPC++
		return connection.RPCID("rpc-" + string(rune('0'+nextRPC))), nil
	})
	streamContext, cancelStream := context.WithCancel(context.Background())
	received := make(chan StreamRequest[MuxFrame], 4)
	done := make(chan error, 1)
	go func() {
		done <- hub.openMux(streamContext, []*session.Session{conversation}, func(item StreamRequest[MuxFrame]) error {
			received <- item
			return nil
		})
	}()
	select {
	case firstFrame := <-received:
		subscribed, matched := firstFrame.Payload.(SessionSubscribedFrame)
		if !matched || subscribed.LastSeq != 0 {
			t.Fatalf("baseline = %#v", firstFrame)
		}
	case <-time.After(time.Second):
		t.Fatal("baseline timed out")
	}
	projected, err := projectSessionEvent(committed)
	if err != nil {
		t.Fatal(err)
	}
	if err := hub.sessionEvent(conversation.ID(), projected, false, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case duplicate := <-received:
		t.Fatalf("late callback duplicated baseline event: %#v", duplicate)
	case <-time.After(20 * time.Millisecond):
	}
	cancelStream()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSessionFrameHubSurfacesMintFailureWithoutAdvancingHighwater(t *testing.T) {
	t.Parallel()
	mintFailure := errors.New("entropy unavailable")
	hub := newSessionFrameHub(func() (connection.RPCID, error) {
		return "", mintFailure
	})
	subscriber := &muxSubscriber{
		queue: newStreamDeliveryQueue[StreamRequest[MuxFrame]](),
		highwater: map[session.SessionID]int64{
			"session-1": -1,
		},
	}
	hub.mux[subscriber] = struct{}{}
	err := hub.sessionEvent("session-1", SessionEvent{Type: "turn/start", Seq: 0}, false, nil)
	if !errors.Is(err, mintFailure) {
		t.Fatalf("frame error = %v, want wrapped mint failure", err)
	}
	if subscriber.highwater["session-1"] != -1 {
		t.Fatalf("highwater = %d, want -1", subscriber.highwater["session-1"])
	}
}
