package basic

import (
	"context"
	"errors"

	"github.com/gorenx/goren/compaction"
	"github.com/gorenx/goren/llm/tokenmeter"
	"github.com/gorenx/goren/session"
)

const surfaceReadAttempts = 3

// surfaceReading joins pricing and Surface facts from exactly one log revision.
type surfaceReading struct {
	snapshot    session.Snapshot
	measurement tokenmeter.Measurement
	boundaries  compaction.ToolPairingBoundaries
}

func readSurface(
	requestContext context.Context,
	conversation session.Context,
	pricing tokenmeter.Meter,
	current *tokenmeter.Measurement,
) (surfaceReading, error) {
	if conversation == nil {
		return surfaceReading{}, errors.New("compaction-basic: reading Session is nil")
	}
	for attemptIndex := 0; attemptIndex < surfaceReadAttempts; attemptIndex++ {
		var measurement tokenmeter.Measurement
		if current != nil {
			measurement = *current
			current = nil
		} else {
			measured, err := pricing.Measure(requestContext, conversation, nil)
			if err != nil {
				return surfaceReading{}, err
			}
			measurement = measured
		}
		snapshot := conversation.Snapshot()
		if measurement.LogRevision != snapshot.Barrier.NextSeq {
			continue
		}
		if len(measurement.Nodes) != len(snapshot.Surface.Nodes) {
			continue
		}
		matches := true
		for nodeIndex, pricedNode := range measurement.Nodes {
			if pricedNode.Seq != snapshot.Surface.Nodes[nodeIndex] {
				matches = false
				break
			}
		}
		if !matches {
			continue
		}
		boundaries, err := compaction.BuildToolPairingBoundaries(snapshot)
		if err != nil {
			return surfaceReading{}, err
		}
		return surfaceReading{
			snapshot:    snapshot,
			measurement: measurement,
			boundaries:  boundaries,
		}, nil
	}
	return surfaceReading{}, changedSurface(
		"compaction: Session changed while reading the Surface",
	)
}

func selectRange(
	reading surfaceReading,
	retainTokens int64,
) (*compaction.SurfaceRange, error) {
	if retainTokens < 0 {
		return nil, errors.New("compaction-basic: retained token budget is negative")
	}
	pricedNodes := reading.measurement.Nodes
	if len(pricedNodes) == 0 {
		return nil, nil
	}
	accumulated := int64(0)
	keepFrom := len(pricedNodes)
	for nodeIndex := len(pricedNodes) - 1; nodeIndex >= 0; nodeIndex-- {
		if pricedNodes[nodeIndex].Tokens < 0 ||
			pricedNodes[nodeIndex].Tokens > int64(1<<53-1)-accumulated {
			return nil, errors.New("compaction-basic: invalid retained-tail token price")
		}
		accumulated += pricedNodes[nodeIndex].Tokens
		keepFrom = nodeIndex
		if accumulated >= retainTokens {
			break
		}
	}
	if keepFrom == 0 {
		return nil, nil
	}
	for keepFrom > 0 {
		balanced, err := reading.boundaries.CutBefore(
			reading.snapshot.Surface.Nodes[keepFrom],
		)
		if err != nil {
			return nil, err
		}
		if balanced {
			break
		}
		keepFrom--
	}
	if keepFrom == 0 {
		return nil, nil
	}
	return &compaction.SurfaceRange{
		Start: reading.snapshot.Surface.Nodes[0],
		End:   reading.snapshot.Surface.Nodes[keepFrom-1],
	}, nil
}
