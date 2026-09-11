package agent_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	agentcore "github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
)

func userMessage(t *testing.T, text string) agentmessage.UserMessage {
	t.Helper()
	message, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
		Content: []agentmessage.ContentBlock{
			agentmessage.TextBlock{
				Type: "text",
				Text: text,
			},
		},
		Source: agentmessage.UserMessageSource{
			Kind: "user",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return message
}

func inboxFixture(t *testing.T) (*session.Log, *agentcore.Inbox) {
	t.Helper()
	conversation, err := session.NewLog("inbox", session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := agentcore.NewInbox(conversation.Header(), conversation.Events())
	if err != nil {
		t.Fatal(err)
	}
	return conversation, pending
}

func applyInboxEdit(t *testing.T, conversation *session.Log, pending *agentcore.Inbox, edit agentcore.InboxEdit) agentcore.InboxChanges {
	t.Helper()
	committed, err := conversation.Commit(context.Background(), session.Batch(edit.Drafts...))
	if err != nil {
		t.Fatal(err)
	}
	changes, err := pending.Apply(committed.Events)
	if err != nil {
		t.Fatal(err)
	}
	return changes
}

func appendInbox(t *testing.T, conversation *session.Log, pending *agentcore.Inbox, target agentcore.InboxTarget, message agentmessage.UserMessage) {
	t.Helper()
	edit, err := pending.PrepareAppend(target, message)
	if err != nil {
		t.Fatal(err)
	}
	changes := applyInboxEdit(t, conversation, pending, edit)
	if len(changes.Inserted) != 1 || changes.Inserted[0].StableID() != message.StableID() {
		t.Fatalf("inserted = %#v", changes)
	}
}

func messageIDs(messages []agentmessage.UserMessage) []agentmessage.MessageID {
	result := make([]agentmessage.MessageID, len(messages))
	for index, message := range messages {
		result[index] = message.StableID()
	}
	return result
}

func TestInboxPreparationDoesNotApplyUncommittedWork(t *testing.T) {
	conversation, pending := inboxFixture(t)
	message := userMessage(t, "pending")
	edit, err := pending.PrepareAppend(agentcore.NextTurn, message)
	if err != nil {
		t.Fatal(err)
	}
	if pending.HasPending() || len(conversation.Events()) != 0 {
		t.Fatal("preparation changed live state")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := conversation.Commit(cancelled, session.Batch(edit.Drafts...)); err == nil {
		t.Fatal("cancelled commit succeeded")
	}
	if pending.HasPending() || len(conversation.Events()) != 0 {
		t.Fatal("failed commit changed state")
	}
	committed, err := conversation.Commit(context.Background(), session.Batch(edit.Drafts...))
	if err != nil {
		t.Fatal(err)
	}
	if pending.HasPending() {
		t.Fatal("commit implicitly mutated Inbox")
	}
	if _, err := pending.Apply(committed.Events); err != nil {
		t.Fatal(err)
	}
	recovered, err := agentcore.NewInbox(conversation.Header(), conversation.Events())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(messageIDs(recovered.NextTurn()), []agentmessage.MessageID{message.StableID()}) {
		t.Fatal("recovery differs")
	}
}

func TestInboxReplacementAndClaimCommitAsOneBatch(t *testing.T) {
	conversation, pending := inboxFixture(t)
	turn := userMessage(t, "turn")
	step := userMessage(t, "step")
	replacement := userMessage(t, "replacement")
	appendInbox(t, conversation, pending, agentcore.NextTurn, turn)
	appendInbox(t, conversation, pending, agentcore.NextStep, step)
	edit, err := pending.PrepareReplace(turn.StableID(), replacement)
	if err != nil {
		t.Fatal(err)
	}
	changes := applyInboxEdit(t, conversation, pending, edit)
	if !reflect.DeepEqual(messageIDs(changes.Discarded), []agentmessage.MessageID{turn.StableID()}) {
		t.Fatal("replacement did not discard old message")
	}
	edit, err = pending.PrepareClaim(agentcore.NextTurn)
	if err != nil {
		t.Fatal(err)
	}
	want := []agentmessage.MessageID{step.StableID(), replacement.StableID()}
	if len(edit.Drafts) != 2 || !reflect.DeepEqual(messageIDs(edit.Removed), want) {
		t.Fatalf("claim = %#v", edit)
	}
	if !pending.HasPending() {
		t.Fatal("preparation claimed messages early")
	}
	changes = applyInboxEdit(t, conversation, pending, edit)
	if pending.HasPending() || !reflect.DeepEqual(messageIDs(changes.Claimed), want) || len(changes.Discarded) != 0 {
		t.Fatalf("claim changes = %#v", changes)
	}
	entries := conversation.Events()
	if len(entries) != 5 {
		t.Fatalf("events = %d", len(entries))
	}
	var mutation agentcore.InboxSplice
	if err := json.Unmarshal(entries[4].Data, &mutation); err != nil {
		t.Fatal(err)
	}
	if mutation.Outcome != "" || mutation.Inserted == nil {
		t.Fatalf("claim wire = %#v", mutation)
	}
}

func TestInboxClearOrdersCancellationsAndRejectsDuplicates(t *testing.T) {
	conversation, pending := inboxFixture(t)
	turn := userMessage(t, "turn")
	step := userMessage(t, "step")
	appendInbox(t, conversation, pending, agentcore.NextTurn, turn)
	appendInbox(t, conversation, pending, agentcore.NextStep, step)
	if _, err := pending.PrepareAppend(agentcore.NextStep, turn); err == nil {
		t.Fatal("duplicate identity accepted across lists")
	}
	edit, err := pending.PrepareClear()
	if err != nil {
		t.Fatal(err)
	}
	if len(edit.Drafts) != 2 {
		t.Fatal("clear must prepare both lists together")
	}
	changes := applyInboxEdit(t, conversation, pending, edit)
	want := []agentmessage.MessageID{step.StableID(), turn.StableID()}
	if !reflect.DeepEqual(messageIDs(changes.Discarded), want) || len(changes.Claimed) != 0 || pending.HasPending() {
		t.Fatalf("clear = %#v", changes)
	}
	edit, err = pending.PrepareClear()
	if err != nil || len(edit.Drafts) != 0 {
		t.Fatalf("empty clear = %#v, %v", edit, err)
	}
}

func TestInboxApplyRejectsWholeInvalidBatch(t *testing.T) {
	conversation, pending := inboxFixture(t)
	first := userMessage(t, "first")
	edit, err := pending.PrepareAppend(agentcore.NextTurn, first)
	if err != nil {
		t.Fatal(err)
	}
	committed, err := conversation.Commit(context.Background(), session.Batch(edit.Drafts...))
	if err != nil {
		t.Fatal(err)
	}
	entries := append([]session.Event(nil), committed.Events...)
	entries = append(entries, committed.Events[0].Clone())
	entries[1].Seq++
	if _, err := pending.Apply(entries); err == nil {
		t.Fatal("duplicate splice accepted")
	}
	if pending.HasPending() {
		t.Fatal("invalid batch partially applied")
	}
	if _, err := agentcore.NewInbox(conversation.Header(), entries); err == nil {
		t.Fatal("invalid durable batch restored")
	}
	if _, err := pending.Apply(committed.Events); err != nil {
		t.Fatal(err)
	}
}

func TestInboxRecoverySkipsSeedHistory(t *testing.T) {
	conversation, pending := inboxFixture(t)
	appendInbox(t, conversation, pending, agentcore.NextTurn, userMessage(t, "inherited"))
	metadata := conversation.Header()
	boundary := int64(1)
	metadata.SeedLength = &boundary
	restored, err := agentcore.NewInbox(metadata, conversation.Events())
	if err != nil {
		t.Fatal(err)
	}
	if restored.HasPending() {
		t.Fatal("inherited work was replayed")
	}
	boundary = 2
	if _, err := agentcore.NewInbox(metadata, conversation.Events()); err == nil {
		t.Fatal("missing seed history accepted")
	}
}
