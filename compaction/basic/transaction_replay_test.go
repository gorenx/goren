package basic

import (
	"context"
	"reflect"
	"testing"

	"github.com/gorenx/goren/compaction"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

func TestTransactionPreservesRequestMetadataAndReplayableCheckpoint(t *testing.T) {
	history, selected := transactionSession(t)
	systemText := "conversation instructions"
	header, err := session.NewEventDraft(session.RequestHeaderSet, session.RequestHeaderSnapshot{
		Header: session.EpochHeader{
			Config: llm.CallConfig{
				Provider: "conversation",
				Model:    "main",
			},
			System: &systemText,
			Tools: []llm.ToolSchema{{
				Name:        "lookup",
				Description: "lookup data",
				Parameters:  []byte(`{"type":"object"}`),
			}},
		},
		Reason: session.RequestHeaderInitial,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := history.Commit(context.Background(), session.Batch(header)); err != nil {
		t.Fatal(err)
	}
	settings, err := ResolveConfig(Config{})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := history.Snapshot()
	execution, err := NewTransaction(settings, TransactionInput{
		Lifecycle: compaction.Start{
			CompactionID: "replay",
		},
		Snapshot:    snapshot,
		Measurement: flowPrices(snapshot, 30),
		Range:       selected,
		Fallback: RouteTarget{
			Provider: "fallback",
			Model:    "unused",
		},
	}, &checkpointEstimate{
		tokens: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	commitTransaction(t, history, execution)
	request, err := execution.Generation()
	if err != nil {
		t.Fatal(err)
	}
	if request.Provider != "conversation" || request.Model != "main" || request.System == nil || *request.System != systemText || len(request.Tools) != 1 || request.Tools[0].Name != "lookup" || len(request.Messages) != 3 || request.Purpose != llm.PurposeCompaction {
		t.Fatalf("request = %#v", request)
	}
	if err := execution.Opened(llm.CallConfig{
		Provider:  "actual",
		Model:     "resolved",
		MaxTokens: intPointer(100),
	}, nil); err != nil {
		t.Fatal(err)
	}
	for _, chunk := range []llm.StreamChunk{
		llm.ReasoningDeltaChunk{
			Index: 0,
			Text:  "reasoning",
		},
		llm.TextDeltaChunk{
			Index: 1,
			Text:  "checkpoint",
		},
		llm.UsageChunk{
			Usage: llm.TokenUsage{
				InputTokens:  40,
				OutputTokens: 8,
			},
		},
	} {
		if err := execution.Push(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if err := execution.EndGeneration(nil); err != nil {
		t.Fatal(err)
	}
	if err := execution.ConfirmClose(nil); err != nil {
		t.Fatal(err)
	}
	commitTransaction(t, history, execution)
	if err := execution.ConfirmFlush(history.Snapshot().Barrier, nil); err != nil {
		t.Fatal(err)
	}
	result, err := execution.Result()
	if err != nil {
		t.Fatal(err)
	}
	snapshot = history.Snapshot()
	generated, err := compaction.DecodeSummary(snapshot.Events[result.SummarySeq].Data)
	if err != nil {
		t.Fatal(err)
	}
	if generated.Provider != "actual" || generated.Model != "resolved" || generated.MaxTokens == nil || *generated.MaxTokens != 100 || generated.Usage == nil || generated.Usage.InputTokens != 40 || len(generated.RawOutput) != 2 || len(generated.Summary) != 1 {
		t.Fatalf("summary = %#v", generated)
	}
	expected := append([]int64{result.StartSeq, result.SummarySeq}, result.ShadowedSeqs...)
	replacement := snapshot.Events[result.SummarySeq+1]
	if replacement.SourceEventSeqs == nil || !reflect.DeepEqual(*replacement.SourceEventSeqs, expected) {
		t.Fatalf("sources = %#v", replacement.SourceEventSeqs)
	}
	replayed, err := session.NewLog("replayed", session.CreateOptions{
		Seed: snapshot.Events,
	})
	if err != nil {
		t.Fatal(err)
	}
	original, err := history.DeriveMessages()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := replayed.DeriveMessages()
	if err != nil {
		t.Fatal(err)
	}
	if len(original) != len(restored) || len(restored) == 0 || !compaction.IsCheckpointSource(restored[0].SourceValue()) || !reflect.DeepEqual(original[0].ContentValue(), restored[0].ContentValue()) {
		t.Fatal("checkpoint replay differs")
	}
}
