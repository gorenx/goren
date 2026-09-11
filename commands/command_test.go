package commands

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/gorenx/goren/session"
	"testing"
)

func commandFixture(t *testing.T, attachments int) (*Command, *session.Log) {
	t.Helper()
	history, err := session.NewLog("conversation", session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	subject, err := NewCommand(Invocation{
		CommandID: "cmd-test-1",
		SessionID: "conversation",
		RawInput:  " raw ",
	}, Definition{
		Name:        "compact",
		Description: "Compact history",
	}, ExecuteOptions{
		AttachmentCount: attachments,
	})
	if err != nil {
		t.Fatal(err)
	}
	return subject, history
}

func TestCommandCommitAdmissionAndIdempotentSettlement(t *testing.T) {
	subject, history := commandFixture(t, 0)
	if _, err := subject.Invocation(); err == nil {
		t.Fatal("handler admitted before run commit")
	}
	opening, err := subject.RunWrite()
	if err != nil {
		t.Fatal(err)
	}
	committed, err := history.Commit(context.Background(), opening)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := history.Commit(context.Background(), opening)
	if err != nil || retry.FirstSeq != committed.FirstSeq {
		t.Fatalf("run retry: %#v, %v", retry, err)
	}
	invalid := committed
	invalid.Barrier.SessionID = "other"
	if err := subject.ConfirmRun(invalid); err == nil {
		t.Fatal("wrong Session accepted")
	}
	if err := subject.ConfirmRun(committed); err != nil {
		t.Fatal(err)
	}
	if _, err := subject.Invocation(); err != nil {
		t.Fatal(err)
	}
	text := "complete"
	if err := subject.Resolve(Result{
		Kind: ResultSuccess,
		Text: &text,
	}, history.Snapshot()); err != nil {
		t.Fatal(err)
	}
	text = "mutated"
	if err := subject.Fail(errors.New("late cancel")); err == nil {
		t.Fatal("late cancellation replaced result")
	}
	closing, err := subject.DoneWrite()
	if err != nil {
		t.Fatal(err)
	}
	committed, err = history.Commit(context.Background(), closing)
	if err != nil {
		t.Fatal(err)
	}
	retry, err = history.Commit(context.Background(), closing)
	if err != nil || retry.FirstSeq != committed.FirstSeq || len(history.Snapshot().Events) != 2 {
		t.Fatalf("done retry: %#v, %v", retry, err)
	}
	if err := subject.ConfirmDone(committed); err != nil {
		t.Fatal(err)
	}
	result, err := subject.Result()
	if err != nil || result.Result.Text == nil || *result.Result.Text != "complete" {
		t.Fatalf("result: %#v, %v", result, err)
	}
}

func TestCommandCancellationBeforeRunAcknowledgement(t *testing.T) {
	subject, history := commandFixture(t, 0)
	cause := errors.New("cancelled by caller")
	if err := subject.Cancel(cause); err != nil {
		t.Fatal(err)
	}
	opening, err := subject.RunWrite()
	if err != nil {
		t.Fatal(err)
	}
	committed, err := history.Commit(context.Background(), opening)
	if err != nil {
		t.Fatal(err)
	}
	if err := subject.ConfirmRun(committed); err != nil {
		t.Fatal(err)
	}
	if _, err := subject.Invocation(); err == nil {
		t.Fatal("cancelled command admitted handler")
	}
	closing, err := subject.DoneWrite()
	if err != nil {
		t.Fatal(err)
	}
	committed, err = history.Commit(context.Background(), closing)
	if err != nil {
		t.Fatal(err)
	}
	if err := subject.ConfirmDone(committed); err != nil {
		t.Fatal(err)
	}
	result, err := subject.Result()
	if !errors.Is(err, cause) || result.Result.Kind != ResultError {
		t.Fatalf("cancellation: %#v, %v", result, err)
	}
}

func TestCommandRejectsAttachmentsAfterRunAndConflictingReplay(t *testing.T) {
	subject, history := commandFixture(t, 1)
	opening, err := subject.RunWrite()
	if err != nil {
		t.Fatal(err)
	}
	committed, err := history.Commit(context.Background(), opening)
	if err != nil {
		t.Fatal(err)
	}
	if err := subject.ConfirmRun(committed); err != nil {
		t.Fatal(err)
	}
	if _, err := subject.Invocation(); err == nil {
		t.Fatal("attachments reached handler")
	}
	if _, err := subject.DoneWrite(); err != nil {
		t.Fatal(err)
	}
	conflicting, err := NewCommand(Invocation{
		CommandID: "cmd-test-1",
		SessionID: "conversation",
		RawInput:  "different",
	}, Definition{
		Name: "compact",
	}, ExecuteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := conflicting.RunWrite()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := history.Commit(context.Background(), repeated); err == nil {
		t.Fatal("conflicting identity accepted")
	}
}

func TestCommandCanSuppressRecordedInput(t *testing.T) {
	history, err := session.NewLog("conversation", session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	record := false
	subject, err := NewCommand(Invocation{
		CommandID: "cmd-no-input",
		SessionID: "conversation",
		RawInput:  "omitted input",
	}, Definition{
		Name:        "compact",
		RecordInput: &record,
	}, ExecuteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	opening, err := subject.RunWrite()
	if err != nil {
		t.Fatal(err)
	}
	written, err := history.Commit(context.Background(), opening)
	if err != nil {
		t.Fatal(err)
	}
	var event Run
	if err := json.Unmarshal(written.Events[0].Data, &event); err != nil {
		t.Fatal(err)
	}
	if event.Args != nil {
		t.Fatal("input was recorded")
	}
}
