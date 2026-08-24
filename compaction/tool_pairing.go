package compaction

import (
	"errors"
	"fmt"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

type toolPairingBalance struct {
	cutBalanced []bool
	indexBySeq  map[int64]int
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
	balanced, err := buildToolPairingBalance(snapshot)
	if err != nil {
		return false, err
	}
	return balanced.cut(sequence, 0)
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
	balanced, err := buildToolPairingBalance(snapshot)
	if err != nil {
		return false, err
	}
	return balanced.cut(sequence, 1)
}

func buildToolPairingBalance(snapshot session.Snapshot) (toolPairingBalance, error) {
	balanced := toolPairingBalance{
		cutBalanced: []bool{true},
		indexBySeq:  make(map[int64]int, len(snapshot.Surface.Nodes)),
	}
	inProgress := 0
	for index, sequence := range snapshot.Surface.Nodes {
		entry, err := eventAtSequence(snapshot.Events, sequence)
		if err != nil {
			return toolPairingBalance{}, err
		}
		delta, err := toolPairingDelta(entry)
		if err != nil {
			return toolPairingBalance{}, err
		}
		inProgress += delta
		if inProgress < 0 {
			return toolPairingBalance{}, fmt.Errorf(
				"tool-pairing balance: tool/result at surface seq %d has no matching tool-call (corrupt surface)",
				sequence,
			)
		}
		balanced.indexBySeq[sequence] = index
		balanced.cutBalanced = append(balanced.cutBalanced, inProgress == 0)
	}
	return balanced, nil
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

func (balanced toolPairingBalance) cut(sequence int64, offset int) (bool, error) {
	index, found := balanced.indexBySeq[sequence]
	if !found || index+offset < 0 || index+offset >= len(balanced.cutBalanced) {
		return false, fmt.Errorf(
			"tool-pairing balance: surface seq %d not found",
			sequence,
		)
	}
	return balanced.cutBalanced[index+offset], nil
}
