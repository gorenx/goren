package agent_test

import (
	"encoding/json"
	"reflect"
	"sync"
	"testing"

	agentcore "github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

type inboxRecorder struct {
	mu        sync.Mutex
	inserted  []llm.MessageID
	discarded []llm.MessageID
	claimed   []llm.MessageID
	turns     []int64
}

func (records *inboxRecorder) Inserted(message llm.UserMessage) {
	records.mu.Lock()
	defer records.mu.Unlock()
	records.inserted = append(records.inserted, message.StableID())
}

func (records *inboxRecorder) Discarded(message llm.UserMessage) {
	records.mu.Lock()
	defer records.mu.Unlock()
	records.discarded = append(records.discarded, message.StableID())
}

func (records *inboxRecorder) Claimed(message llm.UserMessage, turn int64) {
	records.mu.Lock()
	defer records.mu.Unlock()
	records.claimed = append(records.claimed, message.StableID())
	records.turns = append(records.turns, turn)
}

func userMessage(t *testing.T, text string) llm.UserMessage {
	t.Helper()
	message, err := llm.NewUserMessage(llm.UserMessageInput{
		Content: []llm.ContentBlock{
			llm.TextBlock{
				Type: "text",
				Text: text,
			},
		},
		Source: llm.UserMessageSource{
			Kind: "user",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return message
}

func TestInboxReplaysAndRejectsInvalidDurableSplices(t *testing.T) {
	t.Parallel()
	conversation, err := session.New("inbox-replay", session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	first := userMessage(t, "first")
	if _, err := session.Append(conversation, agentcore.InboxSpliced, agentcore.InboxSplice{
		Target:   agentcore.NextTurn,
		Start:    0,
		Inserted: []llm.UserMessage{first},
	}); err != nil {
		t.Fatal(err)
	}
	pending, err := agentcore.NewInbox(conversation, &inboxRecorder{})
	if err != nil {
		t.Fatal(err)
	}
	if got := pending.NextTurn(); len(got) != 1 || got[0].StableID() != first.StableID() {
		t.Fatalf("replayed next-turn = %#v", got)
	}

	broken, err := session.New("inbox-broken", session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Append(broken, agentcore.InboxSpliced, agentcore.InboxSplice{
		Target:   agentcore.NextTurn,
		Start:    1,
		Inserted: []llm.UserMessage{},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := agentcore.NewInbox(broken, &inboxRecorder{}); err == nil {
		t.Fatal("invalid persisted splice was accepted")
	}
}

func TestInboxMutationsCommitBeforeNotifications(t *testing.T) {
	t.Parallel()
	conversation, err := session.New("inbox-mutations", session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	records := &inboxRecorder{}
	pending, err := agentcore.NewInbox(conversation, records)
	if err != nil {
		t.Fatal(err)
	}
	turnMessage := userMessage(t, "turn")
	stepMessage := userMessage(t, "step")
	replacement := userMessage(t, "replacement")
	if err := pending.Append(agentcore.NextTurn, turnMessage); err != nil {
		t.Fatal(err)
	}
	if err := pending.Append(agentcore.NextStep, stepMessage); err != nil {
		t.Fatal(err)
	}
	changed, err := pending.Replace(turnMessage.StableID(), replacement)
	if err != nil || !changed {
		t.Fatalf("replace = %t, %v", changed, err)
	}
	claimedMessages, err := pending.Claim(agentcore.NextTurn, 7)
	if err != nil {
		t.Fatal(err)
	}
	wantClaimed := []llm.MessageID{stepMessage.StableID(), replacement.StableID()}
	gotClaimed := []llm.MessageID{claimedMessages[0].StableID(), claimedMessages[1].StableID()}
	if !reflect.DeepEqual(gotClaimed, wantClaimed) {
		t.Fatalf("claimed = %#v, want %#v", gotClaimed, wantClaimed)
	}
	if pending.HasPending() {
		t.Fatal("Inbox remains pending after claim")
	}
	if !reflect.DeepEqual(records.claimed, wantClaimed) || !reflect.DeepEqual(records.turns, []int64{7, 7}) {
		t.Fatalf("claim notifications = %#v / %#v", records.claimed, records.turns)
	}
	if !reflect.DeepEqual(records.discarded, []llm.MessageID{turnMessage.StableID()}) {
		t.Fatalf("discard notifications = %#v", records.discarded)
	}

	entries := conversation.Events()
	if len(entries) != 5 {
		t.Fatalf("durable event count = %d, want 5", len(entries))
	}
	var claimSplice agentcore.InboxSplice
	if err := json.Unmarshal(entries[len(entries)-1].Data, &claimSplice); err != nil {
		t.Fatal(err)
	}
	if claimSplice.Outcome != "" || claimSplice.RemovedCount == nil || *claimSplice.RemovedCount != 1 || claimSplice.Inserted == nil {
		t.Fatalf("claim splice = %#v", claimSplice)
	}
}

func TestInboxClearOrdersCancellationsAndRejectsDuplicates(t *testing.T) {
	t.Parallel()
	conversation, err := session.New("inbox-clear", session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	records := &inboxRecorder{}
	pending, err := agentcore.NewInbox(conversation, records)
	if err != nil {
		t.Fatal(err)
	}
	turnMessage := userMessage(t, "turn")
	stepMessage := userMessage(t, "step")
	if err := pending.Append(agentcore.NextTurn, turnMessage); err != nil {
		t.Fatal(err)
	}
	if err := pending.Append(agentcore.NextStep, stepMessage); err != nil {
		t.Fatal(err)
	}
	if err := pending.Append(agentcore.NextStep, turnMessage); err == nil {
		t.Fatal("duplicate pending identity was accepted")
	}
	if err := pending.Clear(); err != nil {
		t.Fatal(err)
	}
	if want := []llm.MessageID{stepMessage.StableID(), turnMessage.StableID()}; !reflect.DeepEqual(records.discarded, want) {
		t.Fatalf("discard order = %#v, want %#v", records.discarded, want)
	}
	entries := conversation.Events()
	for _, index := range []int{2, 3} {
		var splice agentcore.InboxSplice
		if err := json.Unmarshal(entries[index].Data, &splice); err != nil {
			t.Fatal(err)
		}
		if splice.Outcome != agentcore.InboxCanceled {
			t.Fatalf("clear event %d outcome = %q", index, splice.Outcome)
		}
	}
	before := len(entries)
	if err := pending.Clear(); err != nil {
		t.Fatal(err)
	}
	if len(conversation.Events()) != before {
		t.Fatal("empty clear appended events")
	}
}

func TestInboxConcurrentAppendsRemainDurableAndUnique(t *testing.T) {
	conversation, err := session.New("inbox-concurrent", session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := agentcore.NewInbox(conversation, &inboxRecorder{})
	if err != nil {
		t.Fatal(err)
	}
	messages := make([]llm.UserMessage, 32)
	for index := range messages {
		messages[index] = userMessage(t, "concurrent")
	}
	var group sync.WaitGroup
	errorsByIndex := make([]error, len(messages))
	group.Add(len(messages))
	for index := range messages {
		go func(messageIndex int) {
			defer group.Done()
			errorsByIndex[messageIndex] = pending.Append(agentcore.NextTurn, messages[messageIndex])
		}(index)
	}
	group.Wait()
	for _, appendErr := range errorsByIndex {
		if appendErr != nil {
			t.Fatal(appendErr)
		}
	}
	if len(pending.NextTurn()) != len(messages) || len(conversation.Events()) != len(messages) {
		t.Fatalf("concurrent state/events = %d/%d", len(pending.NextTurn()), len(conversation.Events()))
	}
}
