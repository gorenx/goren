package visiblecontext

import (
	"context"
	"testing"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
)

func TestVisibleContextDeduplicatesRetainedSnapshot(t *testing.T) {
	conversation, err := session.New(
		"visible-context-test",
		session.CreateOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	visible, err := New(conversation)
	if err != nil {
		t.Fatal(err)
	}
	contextMessage, present, err := visible.Message("cwd: /workspace", nil)
	if err != nil || !present {
		t.Fatalf("first snapshot = %t, %v", present, err)
	}
	draft, err := session.NewSurfaceEventDraft(
		session.UserMessageAdded,
		contextMessage,
		session.SurfaceIntent{
			Operation: session.SurfaceAppend(),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := conversation.Commit(context.Background(), session.Batch(draft))
	if err != nil {
		t.Fatal(err)
	}
	visible.Observe(result.Events[0])
	if _, present, err = visible.Message("cwd: /workspace", nil); err != nil || present {
		t.Fatalf("duplicate snapshot = %t, %v", present, err)
	}
}

func TestVisibleContextEmitsClearedSnapshot(t *testing.T) {
	conversation, err := session.New(
		"visible-context-clear-test",
		session.CreateOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	visible, err := New(conversation)
	if err != nil {
		t.Fatal(err)
	}
	contextMessage, present, err := visible.Message("cwd: /workspace", nil)
	if err != nil || !present {
		t.Fatalf("first snapshot = %t, %v", present, err)
	}
	draft, err := session.NewSurfaceEventDraft(
		session.UserMessageAdded,
		contextMessage,
		session.SurfaceIntent{
			Operation: session.SurfaceAppend(),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := conversation.Commit(context.Background(), session.Batch(draft))
	if err != nil {
		t.Fatal(err)
	}
	visible.Observe(result.Events[0])
	cleared, present, err := visible.Message("", nil)
	if err != nil || !present {
		t.Fatalf("cleared snapshot = %t, %v", present, err)
	}
	blocks := cleared.ContentValue()
	text, ok := blocks[0].(agentmessage.TextBlock)
	if !ok || text.Text != clearedMessage {
		t.Fatalf("cleared content = %#v", blocks)
	}
}

func TestVisibleContextRestoresRetainedSnapshot(t *testing.T) {
	conversation, err := session.New(
		"visible-context-restore-test",
		session.CreateOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := New(conversation)
	if err != nil {
		t.Fatal(err)
	}
	contextMessage, present, err := initial.Message("cwd: /workspace", nil)
	if err != nil || !present {
		t.Fatalf("initial snapshot = %t, %v", present, err)
	}
	draft, err := session.NewSurfaceEventDraft(
		session.UserMessageAdded,
		contextMessage,
		session.SurfaceIntent{
			Operation: session.SurfaceAppend(),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = conversation.Commit(
		context.Background(),
		session.Batch(draft),
	); err != nil {
		t.Fatal(err)
	}
	restored, err := New(conversation)
	if err != nil {
		t.Fatal(err)
	}
	if _, present, err = restored.Message(
		"cwd: /workspace",
		nil,
	); err != nil || present {
		t.Fatalf("restored duplicate = %t, %v", present, err)
	}
}

func TestVisibleContextRematerializesAfterSurfaceReplacement(t *testing.T) {
	conversation, err := session.New(
		"visible-context-replacement-test",
		session.CreateOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	visible, err := New(conversation)
	if err != nil {
		t.Fatal(err)
	}
	contextMessage, present, err := visible.Message("cwd: /workspace", nil)
	if err != nil || !present {
		t.Fatalf("initial snapshot = %t, %v", present, err)
	}
	contextDraft, err := session.NewSurfaceEventDraft(
		session.UserMessageAdded,
		contextMessage,
		session.SurfaceIntent{
			Operation: session.SurfaceAppend(),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	contextResult, err := conversation.Commit(
		context.Background(),
		session.Batch(contextDraft),
	)
	if err != nil {
		t.Fatal(err)
	}
	visible.Observe(contextResult.Events[0])
	replacement, err := agentmessage.NewUserMessage(
		agentmessage.UserMessageInput{
			Content: []agentmessage.ContentBlock{
				agentmessage.NewTextBlock("summary"),
			},
			Source: agentmessage.UserMessageSource{},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	replacedSequences := []int64{contextResult.Events[0].Seq}
	replacementDraft, err := session.NewSurfaceEventDraft(
		session.UserMessageAdded,
		replacement,
		session.SurfaceIntent{
			Operation: session.SurfaceReplace(
				contextResult.Events[0].Seq,
				contextResult.Events[0].Seq,
			),
			SourceEventSeqs: &replacedSequences,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	replacementResult, err := conversation.Commit(
		context.Background(),
		session.Batch(replacementDraft),
	)
	if err != nil {
		t.Fatal(err)
	}
	visible.Observe(replacementResult.Events[0])
	rematerialized, present, err := visible.Message("cwd: /workspace", nil)
	if err != nil || !present {
		t.Fatalf("rematerialized snapshot = %t, %v", present, err)
	}
	blocks := rematerialized.ContentValue()
	text, ok := blocks[0].(agentmessage.TextBlock)
	if !ok || text.Text != "cwd: /workspace" {
		t.Fatalf("rematerialized content = %#v", blocks)
	}
}
