package basic

import (
	"errors"

	"github.com/gorenx/goren/compaction"
	"github.com/gorenx/goren/llm/tokenmeter"
	"github.com/gorenx/goren/session"
)

// baseline fixes the selected history, its price, and the original Surface order.
type baseline struct {
	selected   region
	surface    []int64
	tokenCount int64
	input      summarizationInput
}

func captureBaseline(
	snapshot session.Snapshot,
	measurement tokenmeter.Measurement,
	requested compaction.SurfaceRange,
) (baseline, error) {
	if err := validateMeasurement(snapshot, measurement); err != nil {
		return baseline{}, err
	}
	selected, err := locateRegion(snapshot, requested)
	if err != nil {
		return baseline{}, &surfaceChangedError{
			message: "compaction: selected Surface changed before summarization began",
			cause:   err,
		}
	}
	tokenCount := int64(0)
	for _, priced := range measurement.Nodes[selected.first : selected.last+1] {
		if priced.Tokens > int64(1<<53-1)-tokenCount {
			return baseline{}, errors.New("compaction-basic: invalid shadowed token price")
		}
		tokenCount += priced.Tokens
	}
	input, err := buildSummarizationInput(snapshot, selected.sequences)
	if err != nil {
		return baseline{}, err
	}
	return baseline{
		selected:   selected,
		surface:    append([]int64(nil), snapshot.Surface.Nodes...),
		tokenCount: tokenCount,
		input:      input,
	}, nil
}
