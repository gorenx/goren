package agent

import (
	"encoding/json"
	"fmt"

	"github.com/gorenx/goren/session"
)

// ConsumedWork is the durable account of input consumed by one Agent log.
// End is absent when no closed turn accounts for work in the folded events.
// DroppedUnrun records accepted input canceled before any turn could own it.
type ConsumedWork struct {
	End          *session.TurnEnd
	DroppedUnrun bool
}

// FoldConsumedWork derives work ownership from turn, step, and Inbox events.
// The input may be a complete Agent log or one epoch-owned suffix.
func FoldConsumedWork(events []session.Event) (ConsumedWork, error) {
	stepped := make(map[int64]struct{})
	claimed := make(map[int64]struct{})
	var openTurn *int64
	var ending *session.TurnEnd
	droppedUnrun := false
	for _, committed := range events {
		switch committed.Type {
		case session.TurnStartEventName:
			var start session.TurnStart
			if err := json.Unmarshal(committed.Data, &start); err != nil || start.Turn < 1 {
				return ConsumedWork{}, fmt.Errorf(
					"agent: invalid turn/start at Session seq %d",
					committed.Seq,
				)
			}
			turn := start.Turn
			openTurn = &turn
		case session.StepStartEventName:
			var position session.StepPosition
			if err := json.Unmarshal(committed.Data, &position); err != nil ||
				position.Turn < 1 || position.Step < 1 {
				return ConsumedWork{}, fmt.Errorf(
					"agent: invalid step/start at Session seq %d",
					committed.Seq,
				)
			}
			stepped[position.Turn] = struct{}{}
		case InboxSplicedEventName:
			var mutation InboxSplice
			if err := json.Unmarshal(committed.Data, &mutation); err != nil {
				return ConsumedWork{}, fmt.Errorf(
					"agent: invalid Inbox splice at Session seq %d: %w",
					committed.Seq,
					err,
				)
			}
			if mutation.RemovedCount == nil {
				continue
			}
			if mutation.Outcome == InboxCanceled {
				if len(mutation.Inserted) == 0 {
					droppedUnrun = true
				}
			} else if openTurn != nil {
				claimed[*openTurn] = struct{}{}
			}
		case session.TurnEndEventName:
			var closedTurn session.TurnEnd
			if err := json.Unmarshal(committed.Data, &closedTurn); err != nil {
				return ConsumedWork{}, fmt.Errorf(
					"agent: invalid turn/end at Session seq %d: %w",
					committed.Seq,
					err,
				)
			}
			openTurn = nil
			_, enteredStep := stepped[closedTurn.Turn]
			delete(stepped, closedTurn.Turn)
			_, tookInput := claimed[closedTurn.Turn]
			delete(claimed, closedTurn.Turn)
			if enteredStep || tookInput && accountsForClaim(closedTurn.Reason) {
				snapshot := closedTurn
				ending = &snapshot
				droppedUnrun = false
			}
		}
	}
	return ConsumedWork{
		End:          ending,
		DroppedUnrun: droppedUnrun,
	}, nil
}

func accountsForClaim(ending session.TurnEndReason) bool {
	_, completed := ending.(session.TurnCompleted)
	return !completed
}
