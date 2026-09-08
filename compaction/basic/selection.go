package basic

import (
	"errors"

	"github.com/gorenx/goren/compaction"
	"github.com/gorenx/goren/llm/tokenmeter"
	"github.com/gorenx/goren/session"
)

// selectRange chooses a balanced Surface prefix from one snapshot and its prices.
// A zero retention budget still retains the newest node and its tool pairing.
func selectRange(
	snapshot session.Snapshot,
	measurement tokenmeter.Measurement,
	retainTokens int64,
) (*compaction.SurfaceRange, error) {
	if retainTokens < 0 {
		return nil, errors.New("compaction-basic: retained token budget is negative")
	}
	if err := validateMeasurement(snapshot, measurement); err != nil {
		return nil, err
	}
	boundaries, err := compaction.BuildToolPairingBoundaries(snapshot)
	if err != nil {
		return nil, err
	}
	pricedNodes := measurement.Nodes
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
		balanced, err := boundaries.CutBefore(
			snapshot.Surface.Nodes[keepFrom],
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
		Start: snapshot.Surface.Nodes[0],
		End:   snapshot.Surface.Nodes[keepFrom-1],
	}, nil
}

func validateMeasurement(snapshot session.Snapshot, measurement tokenmeter.Measurement) error {
	if measurement.TotalTokens < 0 || measurement.TotalTokens > int64(1<<53-1) {
		return errors.New("compaction-basic: invalid total token price")
	}
	if snapshot.Barrier.NextSeq != int64(len(snapshot.Events)) ||
		measurement.LogRevision != snapshot.Barrier.NextSeq {
		return errors.New("compaction-basic: selection requires prices from the same log revision")
	}
	if len(measurement.Nodes) != len(snapshot.Surface.Nodes) {
		return errors.New("compaction-basic: selection requires prices for the complete Surface")
	}
	for index, priced := range measurement.Nodes {
		if priced.Seq != snapshot.Surface.Nodes[index] {
			return errors.New("compaction-basic: selection prices do not follow Surface order")
		}
		if priced.Tokens < 0 || priced.Tokens > int64(1<<53-1) {
			return errors.New("compaction-basic: invalid Surface token price")
		}
	}
	return nil
}
