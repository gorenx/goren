package toolresultpruner

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
)

func pruningPlan(t *testing.T) (*session.Log, *Plan, *pricingStub) {
	t.Helper()
	history := newPrunerSession(t, "pruning")
	appendToolResultFixture(t, history, "call", []agentmessage.ContentBlock{agentmessage.NewTextBlock(strings.Repeat("x", 100))}, false, nil, nil)
	pricing := &pricingStub{
		price: 37,
	}
	owner := newPrunerFixture(t, pricing, Config{
		ThresholdChars: pointerToInt(50),
		HeadChars:      pointerToInt(4),
		TailChars:      pointerToInt(3),
	})
	proposal, err := owner.NewPlan(history.Snapshot(), pricing)
	if err != nil {
		t.Fatal(err)
	}
	return history, proposal, pricing
}

func TestPruningPlanReplaysOriginalBatchWithoutRepricing(t *testing.T) {
	history, proposal, pricing := pruningPlan(t)
	original := proposal.Result()
	first, err := history.Commit(context.Background(), proposal)
	if err != nil {
		t.Fatal(err)
	}
	pricing.failOnCall = pricing.estimateHit + 1
	for range 2 {
		repeated, err := history.Commit(context.Background(), proposal)
		if err != nil || !reflect.DeepEqual(first, repeated) {
			t.Fatalf("replay = %#v, %v", repeated, err)
		}
		if !reflect.DeepEqual(original, proposal.Result()) {
			t.Fatal("replay lost original pruning result")
		}
	}
	if history.Snapshot().Barrier != first.Barrier || pricing.estimateHit != 1 {
		t.Fatal("replay appended or repriced")
	}
	// The receipt still points at the original batch after unrelated tail writes.
	appendToolResultFixture(t, history, "later", []agentmessage.ContentBlock{agentmessage.NewTextBlock("small")}, false, nil, nil)
	repeated, err := history.Commit(context.Background(), proposal)
	if err != nil || !reflect.DeepEqual(first, repeated) {
		t.Fatalf("later replay = %#v, %v", repeated, err)
	}
}

func TestPruningPlanRejectsOccupiedPositionAndPartialBatch(t *testing.T) {
	history, proposal, _ := pruningPlan(t)
	appendToolResultFixture(t, history, "later", []agentmessage.ContentBlock{agentmessage.NewTextBlock("small")}, false, nil, nil)
	before := history.Snapshot().Barrier
	if _, err := history.Commit(context.Background(), proposal); err == nil {
		t.Fatal("stale pruning committed over another write")
	}
	if history.Snapshot().Barrier != before {
		t.Fatal("conflict appended pruning")
	}
	history, proposal, _ = pruningPlan(t)
	written, err := history.Commit(context.Background(), proposal)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := history.Snapshot()
	snapshot.Events = snapshot.Events[:written.NextSeq-1]
	snapshot.Barrier.NextSeq--
	if _, err := proposal.FindCommit(snapshot); err == nil {
		t.Fatal("partial pruning batch accepted")
	}
}
