package bound

import (
	"context"
	"errors"
	"fmt"

	"github.com/gorenx/goren/session"
	boundcontract "github.com/gorenx/goren/subagent/bound"
)

func boundCursor(
	events []session.Event,
	definitionName string,
	childID session.SessionID,
	floor int64,
) (int64, error) {
	if floor < 0 || floor > int64(len(events)) {
		return 0, errors.New("subagent: invalid Bound cursor floor")
	}
	nextSeq := floor
	for _, committed := range events {
		if committed.Type != boundcontract.CursorEventName {
			continue
		}
		var cursor boundcontract.Cursor
		if err := decodeInteractionJSON(committed.Data, &cursor); err != nil {
			return 0, fmt.Errorf(
				"subagent: decode Bound cursor at seq %d: %w",
				committed.Seq,
				err,
			)
		}
		if cursor.Name != definitionName && cursor.ChildSessionID != childID {
			continue
		}
		if cursor.Version != boundcontract.EventVersion ||
			cursor.Name != definitionName ||
			cursor.ChildSessionID != childID ||
			cursor.PreviousNextSeq != nextSeq ||
			cursor.NextSeq <= cursor.PreviousNextSeq ||
			cursor.NextSeq > committed.Seq ||
			cursor.ThroughTurn <= 0 ||
			(cursor.Disposition != boundcontract.CursorDelivered &&
				cursor.Disposition != boundcontract.CursorSkipped) {
			return 0, fmt.Errorf(
				"subagent: invalid Bound cursor at seq %d",
				committed.Seq,
			)
		}
		nextSeq = cursor.NextSeq
	}
	return nextSeq, nil
}

type cursorAdvance struct {
	name        string
	childID     session.SessionID
	floor       int64
	expected    int64
	interaction parentInteraction
	disposition boundcontract.CursorDisposition
}

func (advance cursorAdvance) Build(
	_ context.Context,
	snapshot session.Snapshot,
) ([]session.EventDraft, error) {
	current, err := boundCursor(
		snapshot.Events,
		advance.name,
		advance.childID,
		advance.floor,
	)
	if err != nil {
		return nil, err
	}
	if current != advance.expected {
		return nil, fmt.Errorf(
			"subagent: Bound cursor moved to %d while expecting %d",
			current,
			advance.expected,
		)
	}
	draft, err := session.NewEventDraft(
		boundcontract.CursorEvent,
		boundcontract.Cursor{
			Version:         boundcontract.EventVersion,
			Name:            advance.name,
			ChildSessionID:  advance.childID,
			PreviousNextSeq: advance.expected,
			NextSeq:         advance.interaction.nextSeq,
			ThroughTurn:     advance.interaction.turn,
			Disposition:     advance.disposition,
		},
	)
	if err != nil {
		return nil, err
	}
	return []session.EventDraft{draft}, nil
}

var _ session.WritePlan = cursorAdvance{}
