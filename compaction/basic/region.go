package basic

import (
	"fmt"

	"github.com/gorenx/goren/compaction"
	"github.com/gorenx/goren/session"
)

// region is one validated inclusive span of a particular Surface snapshot.
type region struct {
	start     int64
	end       int64
	first     int
	last      int
	sequences []int64
}

type surfaceChangedError struct {
	message string
	cause   error
}

func (problem *surfaceChangedError) Error() string {
	if problem == nil {
		return "compaction: Surface changed"
	}
	return problem.message
}

func (problem *surfaceChangedError) Unwrap() error {
	if problem == nil {
		return nil
	}
	return problem.cause
}

func locateRegion(
	snapshot session.Snapshot,
	requested compaction.SurfaceRange,
) (region, error) {
	boundaries, err := compaction.BuildToolPairingBoundaries(snapshot)
	if err != nil {
		return region{}, err
	}
	return locateRegionWithBoundaries(snapshot, boundaries, requested)
}

func locateRegionWithBoundaries(
	snapshot session.Snapshot,
	boundaries compaction.ToolPairingBoundaries,
	requested compaction.SurfaceRange,
) (region, error) {
	first := indexOfSequence(snapshot.Surface.Nodes, requested.Start)
	if first < 0 {
		return region{}, fmt.Errorf(
			"compactRegion: start seq %d not found in Surface",
			requested.Start,
		)
	}
	last := indexOfSequence(snapshot.Surface.Nodes, requested.End)
	if last < 0 {
		return region{}, fmt.Errorf(
			"compactRegion: end seq %d not found in Surface",
			requested.End,
		)
	}
	if first > last {
		return region{}, fmt.Errorf(
			"compactRegion: start seq %d (position %d) is after end seq %d (position %d) on the Surface",
			requested.Start,
			first,
			requested.End,
			last,
		)
	}
	balanced, err := boundaries.CutBefore(requested.Start)
	if err != nil {
		return region{}, err
	}
	if !balanced {
		return region{}, fmt.Errorf(
			"compactRegion: start seq %d is not a balanced boundary (would split a step's tool-call/result pair)",
			requested.Start,
		)
	}
	balanced, err = boundaries.CutAfter(requested.End)
	if err != nil {
		return region{}, err
	}
	if !balanced {
		return region{}, fmt.Errorf(
			"compactRegion: end seq %d is not a balanced boundary (would split a step, or the step is still open)",
			requested.End,
		)
	}
	return region{
		start: requested.Start,
		end:   requested.End,
		first: first,
		last:  last,
		sequences: append(
			[]int64(nil),
			snapshot.Surface.Nodes[first:last+1]...,
		),
	}, nil
}

func assertWholeSurfaceStable(
	snapshot session.Snapshot,
	source baseline,
) error {
	if len(snapshot.Surface.Nodes) != len(source.measurement.Nodes) {
		return changedSurface("compaction: Session Surface changed during summarization")
	}
	for index, sequence := range snapshot.Surface.Nodes {
		if sequence != source.measurement.Nodes[index].Seq {
			return changedSurface("compaction: Session Surface changed during summarization")
		}
	}
	return nil
}

func assertSelectedRegionStable(
	snapshot session.Snapshot,
	source baseline,
) error {
	current, err := locateRegion(
		snapshot,
		compaction.SurfaceRange{
			Start: source.selected.start,
			End:   source.selected.end,
		},
	)
	if err != nil {
		return &surfaceChangedError{
			message: "compaction: selected span is no longer a valid replacement target",
			cause:   err,
		}
	}
	if !equalSequences(current.sequences, source.selected.sequences) {
		return changedSurface("compaction: selected span changed during summarization")
	}
	if len(current.sequences) != len(source.selectedNodes) {
		return changedSurface("compaction: selected span was rewritten during summarization")
	}
	for index, sequence := range current.sequences {
		if sequence != source.selectedNodes[index].Seq {
			return changedSurface("compaction: selected span was rewritten during summarization")
		}
	}
	return nil
}

func changedSurface(message string) error {
	return &surfaceChangedError{
		message: message,
	}
}

func equalSequences(first []int64, second []int64) bool {
	if len(first) != len(second) {
		return false
	}
	for index, sequence := range first {
		if sequence != second[index] {
			return false
		}
	}
	return true
}

func indexOfSequence(nodes []int64, sequence int64) int {
	for nodeIndex, candidate := range nodes {
		if candidate == sequence {
			return nodeIndex
		}
	}
	return -1
}
