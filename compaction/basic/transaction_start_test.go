package basic

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/compaction"
	"github.com/gorenx/goren/session"
)

func transactionSession(t *testing.T) (*session.Log, compaction.SurfaceRange) {
	t.Helper()
	history, err := session.NewLog("opening", session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		entry, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
			Content: []agentmessage.ContentBlock{agentmessage.NewTextBlock("history")},
			Source:  agentmessage.UserMessageSource{},
		})
		if err != nil {
			t.Fatal(err)
		}
		draft, err := session.NewSurfaceEventDraft(session.UserMessageAdded, entry, session.SurfaceIntent{
			Operation: session.SurfaceAppend(),
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := history.Commit(context.Background(), session.Batch(draft)); err != nil {
			t.Fatal(err)
		}
	}
	nodes := history.Snapshot().Surface.Nodes
	return history, compaction.SurfaceRange{
		Start: nodes[0],
		End:   nodes[1],
	}
}

func TestTransactionStartReplaysOriginalCommitAndRejectsConflictingIdentity(t *testing.T) {
	history, selected := transactionSession(t)
	origin := "command"
	proposal, err := newTransactionStart(compaction.Start{
		CompactionID:    "transaction",
		SourceCommandID: &origin,
	}, selected)
	if err != nil {
		t.Fatal(err)
	}
	origin = "changed after construction"
	first, err := history.Commit(context.Background(), proposal)
	if err != nil {
		t.Fatal(err)
	}
	started, err := compaction.DecodeStart(first.Events[0].Data)
	if err != nil || started.SourceCommandID == nil || *started.SourceCommandID != "command" {
		t.Fatalf("start = %#v, %v", started, err)
	}
	before := history.Snapshot().Barrier.NextSeq
	repeated, err := history.Commit(context.Background(), proposal)
	if err != nil || !reflect.DeepEqual(first, repeated) || history.Snapshot().Barrier.NextSeq != before {
		t.Fatalf("repeat = %#v, %v", repeated, err)
	}

	for _, changed := range []string{"source", "turn"} {
		t.Run(changed, func(t *testing.T) {
			lifecycle := started
			if changed == "source" {
				other := "other-command"
				lifecycle.SourceCommandID = &other
			} else {
				turn := int64(1)
				lifecycle.Turn = &turn
			}
			conflicting, err := newTransactionStart(lifecycle, selected)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := history.Commit(context.Background(), conflicting); err == nil {
				t.Fatal("conflicting start accepted")
			}
			if history.Snapshot().Barrier.NextSeq != before {
				t.Fatal("conflict appended an event")
			}
		})
	}
	other, err := newTransactionStart(compaction.Start{
		CompactionID: "another-transaction",
	}, selected)
	if err != nil {
		t.Fatal(err)
	}
	_, err = history.Commit(context.Background(), other)
	var busy *compaction.ManualError
	if !errors.As(err, &busy) || busy.Code != compaction.ManualErrorBusy {
		t.Fatalf("competing transaction = %v", err)
	}
}

func TestTransactionStartChecksCurrentRegionAndTurn(t *testing.T) {
	for _, invalid := range []string{"region", "turn"} {
		t.Run(invalid, func(t *testing.T) {
			history, selected := transactionSession(t)
			lifecycle := compaction.Start{
				CompactionID: "transaction",
			}
			if invalid == "region" {
				selected.End = 999
			} else {
				turn := int64(1)
				lifecycle.Turn = &turn
			}
			proposal, err := newTransactionStart(lifecycle, selected)
			if err != nil {
				t.Fatal(err)
			}
			before := history.Snapshot().Barrier.NextSeq
			if _, err := history.Commit(context.Background(), proposal); err == nil {
				t.Fatal("invalid start accepted")
			}
			if history.Snapshot().Barrier.NextSeq != before {
				t.Fatal("invalid start changed log")
			}
		})
	}
}
