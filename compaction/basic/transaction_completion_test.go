package basic

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/compaction"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/llm/tokenmeter"
	"github.com/gorenx/goren/session"
)

func completionFixture(t *testing.T) (*session.Log, *transactionCompletion, compaction.Start) {
	t.Helper()
	history, selected := transactionSession(t)
	lifecycle := compaction.Start{
		CompactionID: "transaction",
	}
	opening, err := newTransactionStart(lifecycle, selected)
	if err != nil {
		t.Fatal(err)
	}
	started, err := history.Commit(context.Background(), opening)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := history.Snapshot()
	measurement := tokenmeter.Measurement{
		LogRevision: snapshot.Barrier.NextSeq,
	}
	for _, seq := range snapshot.Surface.Nodes {
		measurement.Nodes = append(measurement.Nodes, tokenmeter.SurfaceNode{
			Seq:    seq,
			Tokens: 10,
		})
	}
	source, err := captureBaseline(snapshot, measurement, selected)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := buildReplacement(lifecycle.CompactionID, nil, source, Summary{
		Content: []agentmessage.ContentBlock{agentmessage.NewTextBlock("checkpoint")},
		Output:  []agentmessage.ContentBlock{agentmessage.NewTextBlock("checkpoint")},
	}, llm.CallConfig{
		Provider: "actual",
		Model:    "resolved",
	}, &checkpointEstimate{
		tokens: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := newTransactionCompletion(started.Events[0], replacement)
	if err != nil {
		t.Fatal(err)
	}
	return history, proposal, lifecycle
}

func TestTransactionCompletionCommitsAndReplaysWholeBatch(t *testing.T) {
	history, proposal, lifecycle := completionFixture(t)
	before := history.Snapshot()
	first, err := history.Commit(context.Background(), proposal)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Events) != 3 || first.Events[0].Type != compaction.SummaryEventName || first.Events[1].Type != session.UserMessageEventName || first.Events[2].Type != compaction.EndEventName {
		t.Fatalf("unexpected completion: %#v", first)
	}
	snapshot := history.Snapshot()
	expected := []int64{first.FirstSeq + 1, before.Surface.Nodes[2]}
	if !reflect.DeepEqual(snapshot.Surface.Nodes, expected) {
		t.Fatalf("surface = %v, want %v", snapshot.Surface.Nodes, expected)
	}
	state, err := compaction.InspectLog(snapshot.Events)
	if err != nil || state.Attempt != nil {
		t.Fatalf("transaction remains active: %#v, %v", state, err)
	}
	replayed, err := history.Commit(context.Background(), proposal)
	if err != nil || !reflect.DeepEqual(first, replayed) || history.Snapshot().Barrier != snapshot.Barrier {
		t.Fatalf("replay = %#v, %v", replayed, err)
	}
	failed, err := newTransactionEnd(lifecycle, errors.New("late failure"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := history.Commit(context.Background(), failed); err == nil {
		t.Fatal("failure replaced successful outcome")
	}
}

func TestTransactionCompletionRejectsIncompleteOrDifferentBatch(t *testing.T) {
	history, proposal, _ := completionFixture(t)
	written, err := history.Commit(context.Background(), proposal)
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"incomplete", "different summary"} {
		t.Run(scenario, func(t *testing.T) {
			snapshot := history.Snapshot()
			if scenario == "incomplete" {
				snapshot.Events = snapshot.Events[:written.FirstSeq+2]
			} else {
				snapshot.Events[written.FirstSeq].Data = []byte(`{}`)
			}
			if _, err := proposal.FindCommit(snapshot); err == nil {
				t.Fatal("invalid success batch accepted")
			}
		})
	}
}

func TestTransactionCompletionRejectsChangedSelectionWithoutPartialCommit(t *testing.T) {
	history, proposal, lifecycle := completionFixture(t)
	entry, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
		Content: []agentmessage.ContentBlock{agentmessage.NewTextBlock("changed")},
		Source:  agentmessage.UserMessageSource{},
	})
	if err != nil {
		t.Fatal(err)
	}
	sources := append([]int64(nil), proposal.selected.sequences...)
	draft, err := session.NewSurfaceEventDraft(session.UserMessageAdded, entry, session.SurfaceIntent{
		SourceEventSeqs: &sources,
		Operation:       session.SurfaceReplace(proposal.selected.start, proposal.selected.end),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := history.Commit(context.Background(), session.Batch(draft)); err != nil {
		t.Fatal(err)
	}
	before := history.Snapshot().Barrier
	_, failure := history.Commit(context.Background(), proposal)
	var changed *surfaceChangedError
	if !errors.As(failure, &changed) {
		t.Fatalf("changed selection error = %v", failure)
	}
	if history.Snapshot().Barrier != before {
		t.Fatal("failed completion partially appended")
	}
	state, err := compaction.InspectLog(history.Snapshot().Events)
	if err != nil || state.Attempt == nil {
		t.Fatalf("transaction lost before failure end: %#v, %v", state, err)
	}
	failed, err := newTransactionEnd(lifecycle, errors.New("selection changed"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := history.Commit(context.Background(), failed); err != nil {
		t.Fatal(err)
	}
	if _, err := history.Commit(context.Background(), proposal); err == nil {
		t.Fatal("success appended after failure end")
	}
}

func TestManualTransactionCompletionAllowsChangesOutsideSelection(t *testing.T) {
	history, proposal, _ := completionFixture(t)
	entry, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
		Content: []agentmessage.ContentBlock{agentmessage.NewTextBlock("new tail")},
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
	appended, err := history.Commit(context.Background(), session.Batch(draft))
	if err != nil {
		t.Fatal(err)
	}
	written, err := history.Commit(context.Background(), proposal)
	if err != nil {
		t.Fatal(err)
	}
	nodes := history.Snapshot().Surface.Nodes
	if len(nodes) != 3 || nodes[0] != written.FirstSeq+1 || nodes[2] != appended.FirstSeq {
		t.Fatalf("unrelated tail lost: %v", nodes)
	}
}
