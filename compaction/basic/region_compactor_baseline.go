package basic

import (
	"context"
	"errors"

	"github.com/gorenx/goren/compaction"
	"github.com/gorenx/goren/llm/tokenmeter"
	"github.com/gorenx/goren/session"
)

// baseline is the exact region, pricing, and replay input used by one summary.
type baseline struct {
	selected      region
	measurement   tokenmeter.Measurement
	selectedNodes []tokenmeter.SurfaceNode
	tokenCount    int64
	input         summarizationInput
}

func (owner *regionCompactor) loadBaseline(
	requestContext context.Context,
	conversation session.Context,
	requested compaction.SurfaceRange,
) (baseline, error) {
	reading, err := readSurface(requestContext, conversation, owner.meter, nil)
	if err != nil {
		return baseline{}, err
	}
	selected, err := locateRegionWithBoundaries(
		reading.snapshot,
		reading.boundaries,
		requested,
	)
	if err != nil {
		return baseline{}, &surfaceChangedError{
			message: "compaction: selected Surface changed before summarization began",
			cause:   err,
		}
	}
	selectedNodes := append(
		[]tokenmeter.SurfaceNode(nil),
		reading.measurement.Nodes[selected.first:selected.last+1]...,
	)
	if len(selectedNodes) != len(selected.sequences) {
		return baseline{}, changedSurface(
			"compaction: selected Surface changed before summarization began",
		)
	}
	tokenCount := int64(0)
	for nodeIndex, pricedNode := range selectedNodes {
		if pricedNode.Seq != selected.sequences[nodeIndex] {
			return baseline{}, changedSurface(
				"compaction: selected Surface changed before summarization began",
			)
		}
		if pricedNode.Tokens < 0 ||
			pricedNode.Tokens > int64(1<<53-1)-tokenCount {
			return baseline{}, errors.New(
				"compaction-basic: invalid shadowed token price",
			)
		}
		tokenCount += pricedNode.Tokens
	}
	input, err := buildSummarizationInput(
		reading.snapshot,
		selected.sequences,
	)
	if err != nil {
		return baseline{}, err
	}
	return baseline{
		selected:      selected,
		measurement:   reading.measurement,
		selectedNodes: selectedNodes,
		tokenCount:    tokenCount,
		input:         input,
	}, nil
}
