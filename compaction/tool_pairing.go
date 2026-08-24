package compaction

import (
	"errors"
	"fmt"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

// ToolPairingBoundaries indexes the balanced cuts of one immutable Session
// snapshot. Build it once when several candidate boundaries must be tested.
type ToolPairingBoundaries struct {
	cutBalanced []bool
	indexBySeq  map[int64]int
}

// BuildToolPairingBoundaries indexes every cut in one Session snapshot.
func BuildToolPairingBoundaries(
	snapshot session.Snapshot,
) (ToolPairingBoundaries, error) {
	boundaries := ToolPairingBoundaries{
		cutBalanced: []bool{true},
		indexBySeq:  make(map[int64]int, len(snapshot.Surface.Nodes)),
	}
	inProgress := 0
	for index, sequence := range snapshot.Surface.Nodes {
		entry, err := eventAtSequence(snapshot.Events, sequence)
		if err != nil {
			return ToolPairingBoundaries{}, err
		}
		delta, err := toolPairingDelta(entry)
		if err != nil {
			return ToolPairingBoundaries{}, err
		}
		inProgress += delta
		if inProgress < 0 {
			return ToolPairingBoundaries{}, fmt.Errorf(
				"tool-pairing balance: tool/result at surface seq %d has no matching tool-call (corrupt surface)",
				sequence,
			)
		}
		boundaries.indexBySeq[sequence] = index
		boundaries.cutBalanced = append(
			boundaries.cutBalanced,
			inProgress == 0,
		)
	}
	return boundaries, nil
}

// ToolPairingBalancedBefore reports whether no unanswered Tool Call crosses
// the cut immediately before one current Surface node.
func ToolPairingBalancedBefore(conversation session.Context, sequence int64) (bool, error) {
	if conversation == nil {
		return false, errors.New("tool-pairing balance: Session is nil")
	}
	return ToolPairingBalancedBeforeSnapshot(conversation.Snapshot(), sequence)
}

// ToolPairingBalancedBeforeSnapshot evaluates one already-consistent Session snapshot.
func ToolPairingBalancedBeforeSnapshot(
	snapshot session.Snapshot,
	sequence int64,
) (bool, error) {
	boundaries, err := BuildToolPairingBoundaries(snapshot)
	if err != nil {
		return false, err
	}
	return boundaries.CutBefore(sequence)
}

// ToolPairingBalancedAfter reports whether no unanswered Tool Call crosses
// the cut immediately after one current Surface node.
func ToolPairingBalancedAfter(conversation session.Context, sequence int64) (bool, error) {
	if conversation == nil {
		return false, errors.New("tool-pairing balance: Session is nil")
	}
	return ToolPairingBalancedAfterSnapshot(conversation.Snapshot(), sequence)
}

// ToolPairingBalancedAfterSnapshot evaluates one already-consistent Session snapshot.
func ToolPairingBalancedAfterSnapshot(
	snapshot session.Snapshot,
	sequence int64,
) (bool, error) {
	boundaries, err := BuildToolPairingBoundaries(snapshot)
	if err != nil {
		return false, err
	}
	return boundaries.CutAfter(sequence)
}

func eventAtSequence(events []session.Event, sequence int64) (session.Event, error) {
	if sequence < 0 || sequence >= int64(len(events)) || events[sequence].Seq != sequence {
		return session.Event{}, fmt.Errorf(
			"tool-pairing balance: surface seq %d has no matching session event (corrupt surface)",
			sequence,
		)
	}
	return events[sequence], nil
}

func toolPairingDelta(entry session.Event) (int, error) {
	switch entry.Type {
	case session.AssistantMessageEventName:
		messageValue, err := session.DeriveEventMessage(entry)
		if err != nil {
			return 0, err
		}
		if messageValue == nil {
			return 0, nil
		}
		delta := 0
		for _, contentBlock := range messageValue.ContentValue() {
			switch contentBlock.(type) {
			case llm.ToolCallBlock, *llm.ToolCallBlock:
				delta++
			}
		}
		return delta, nil
	case session.ToolResultEventName:
		return -1, nil
	default:
		return 0, nil
	}
}

// CutBefore reports whether the cut immediately before one Surface node is balanced.
func (boundaries ToolPairingBoundaries) CutBefore(sequence int64) (bool, error) {
	return boundaries.cut(sequence, 0)
}

// CutAfter reports whether the cut immediately after one Surface node is balanced.
func (boundaries ToolPairingBoundaries) CutAfter(sequence int64) (bool, error) {
	return boundaries.cut(sequence, 1)
}

func (boundaries ToolPairingBoundaries) cut(sequence int64, offset int) (bool, error) {
	index, found := boundaries.indexBySeq[sequence]
	if !found || index+offset < 0 || index+offset >= len(boundaries.cutBalanced) {
		return false, fmt.Errorf(
			"tool-pairing balance: surface seq %d not found",
			sequence,
		)
	}
	return boundaries.cutBalanced[index+offset], nil
}
