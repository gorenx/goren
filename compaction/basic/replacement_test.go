package basic

import (
	"errors"
	"strings"
	"testing"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/compaction"
	"github.com/gorenx/goren/llm"
)

type checkpointEstimate struct {
	tokens   int64
	failure  error
	observed agentmessage.Message
}

func (pricing *checkpointEstimate) EstimateMessage(entry agentmessage.Message) (int64, error) {
	pricing.observed = entry
	return pricing.tokens, pricing.failure
}

func TestReplacementRequiresSmallerFramedCheckpoint(t *testing.T) {
	for _, price := range []int64{-1, 19, 20, 21} {
		snapshot, measurement := selectionSnapshot(t, 3)
		source, err := captureBaseline(snapshot, measurement, compaction.SurfaceRange{
			Start: 0,
			End:   1,
		})
		if err != nil {
			t.Fatal(err)
		}
		pricing := &checkpointEstimate{
			tokens: price,
		}
		candidate, err := buildReplacement("transaction", nil, source, Summary{
			Content: []agentmessage.ContentBlock{agentmessage.NewTextBlock("checkpoint")},
		}, llm.CallConfig{
			Provider: "actual",
			Model:    "resolved",
		}, pricing)
		if (err == nil) != (price == 19) {
			t.Fatalf("price %d: %v", price, err)
		}
		if pricing.observed == nil {
			t.Fatal("framed checkpoint was not priced")
		}
		blocks := pricing.observed.ContentValue()
		if len(blocks) != 3 || !strings.Contains(blocks[0].(agentmessage.TextBlock).Text, summaryOpenTag) || blocks[2].(agentmessage.TextBlock).Text != summaryCloseTag {
			t.Fatal("estimator did not receive checkpoint wrapper")
		}
		if err == nil && (candidate.settings.Provider != "actual" || candidate.settings.Model != "resolved") {
			t.Fatal("actual model metadata lost")
		}
	}
}

func TestReplacementPreservesEstimatorFailure(t *testing.T) {
	failure := errors.New("estimate failed")
	_, err := buildReplacement("transaction", nil, baseline{}, Summary{
		Content: []agentmessage.ContentBlock{agentmessage.NewTextBlock("checkpoint")},
	}, llm.CallConfig{
		Provider: "actual",
		Model:    "resolved",
	}, &checkpointEstimate{
		failure: failure,
	})
	if !errors.Is(err, failure) {
		t.Fatalf("failure = %v", err)
	}
}
