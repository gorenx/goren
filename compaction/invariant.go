package compaction

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gorenx/goren/session"
)

// OpenAttempt is the durable compaction lock reconstructed from one Session
// log. It remains open until a matching compaction/end or session/end-seed.
type OpenAttempt struct {
	CompactionID    ID
	SourceCommandID *string
	StartSeq        int64
	Turn            *int64
	Summarized      bool
}

// LogState is the current turn and compaction owner reconstructed at one log
// cut. Session events remain the source of truth.
type LogState struct {
	OpenTurn *int64
	Attempt  *OpenAttempt
}

// InspectLog validates package-owned cross-event invariants and reconstructs
// the current durable lock. It accepts an open tail so a caller can decide how
// to recover or reject a competing transaction.
func InspectLog(entries []session.Event) (LogState, error) {
	current := LogState{}
	staleStarts := inheritedOrphanStarts(entries)
	for _, entry := range entries {
		if current.Attempt == nil || !staleStarts[current.Attempt.StartSeq] {
			if err := validateCompactionTurnBoundary(current, entry); err != nil {
				return LogState{}, inspectError(entry, err)
			}
		}
		if err := applyCompactionEvent(&current, entry); err != nil {
			return LogState{}, inspectError(entry, err)
		}
		if err := applyCompactionTurnBoundary(&current, entry); err != nil {
			return LogState{}, inspectError(entry, err)
		}
	}
	return cloneLogState(current), nil
}

func inheritedOrphanStarts(entries []session.Event) map[int64]bool {
	stale := make(map[int64]bool)
	var openStartSeq *int64
	for _, entry := range entries {
		switch entry.Type {
		case StartEventName:
			sequence := entry.Seq
			openStartSeq = &sequence
		case EndEventName:
			openStartSeq = nil
		case session.EndSeedEventName:
			if openStartSeq != nil {
				stale[*openStartSeq] = true
			}
			openStartSeq = nil
		}
	}
	return stale
}

func validateCompactionTurnBoundary(current LogState, entry session.Event) error {
	if current.Attempt == nil ||
		(entry.Type != session.TurnStartEventName && entry.Type != session.TurnEndEventName) {
		return nil
	}
	owner := "standalone compaction"
	if current.Attempt.Turn != nil {
		owner = fmt.Sprintf("compaction for turn %d", *current.Attempt.Turn)
	}
	return fmt.Errorf("%s cannot cross an open %s", entry.Type, owner)
}

func applyCompactionEvent(current *LogState, entry session.Event) error {
	if entry.Type == session.EndSeedEventName {
		current.Attempt = nil
		return nil
	}
	if entry.Type == session.UserMessageEventName && entry.SurfaceOp != nil &&
		entry.SurfaceOp.Kind == session.SurfaceOperationReplace {
		if err := validateCompactionCheckpoint(*current, entry); err != nil {
			return err
		}
	}

	switch entry.Type {
	case StartEventName:
		startValue, err := DecodeStart(entry.Data)
		if err != nil {
			return err
		}
		if current.Attempt != nil {
			owner := "standalone compaction"
			if current.Attempt.Turn != nil {
				owner = fmt.Sprintf("turn %d", *current.Attempt.Turn)
			}
			return fmt.Errorf("compaction/start while %s is still compacting", owner)
		}
		if err := validateCompactionOwner(startValue.Turn, current.OpenTurn, entry.Type); err != nil {
			return err
		}
		current.Attempt = &OpenAttempt{
			CompactionID:    startValue.CompactionID,
			SourceCommandID: cloneString(startValue.SourceCommandID),
			StartSeq:        entry.Seq,
			Turn:            cloneTurn(startValue.Turn),
		}
	case SummaryEventName:
		summaryValue, err := DecodeSummary(entry.Data)
		if err != nil {
			return err
		}
		if current.Attempt == nil {
			return errors.New("compaction/summary has no matching compaction/start")
		}
		if summaryValue.CompactionID != current.Attempt.CompactionID {
			return fmt.Errorf(
				"compaction/summary id %s does not match compaction/start id %s",
				summaryValue.CompactionID,
				current.Attempt.CompactionID,
			)
		}
		if err := validateSourceCommandOwner(
			entry.Type,
			summaryValue.SourceCommandID,
			current.Attempt.SourceCommandID,
		); err != nil {
			return err
		}
		if err := validateCompactionOwner(
			current.Attempt.Turn,
			current.OpenTurn,
			entry.Type,
		); err != nil {
			return err
		}
		if current.Attempt.Summarized {
			return errors.New("compaction/summary repeated within one compaction")
		}
		if len(summaryValue.ShadowedSeqs) == 0 {
			return errors.New("compaction/summary shadowedSeqs must be non-empty")
		}
		if summaryValue.ShadowedSeqs[0] != summaryValue.ShadowedRange.Start ||
			summaryValue.ShadowedSeqs[len(summaryValue.ShadowedSeqs)-1] !=
				summaryValue.ShadowedRange.End {
			return errors.New(
				"compaction/summary shadowedRange must match the first and last shadowedSeqs",
			)
		}
		current.Attempt.Summarized = true
	case EndEventName:
		endValue, err := DecodeEnd(entry.Data)
		if err != nil {
			return err
		}
		if current.Attempt == nil {
			return errors.New("compaction/end has no matching compaction/start")
		}
		if endValue.CompactionID != current.Attempt.CompactionID {
			return fmt.Errorf(
				"compaction/end id %s does not match compaction/start id %s",
				endValue.CompactionID,
				current.Attempt.CompactionID,
			)
		}
		if err := validateSourceCommandOwner(
			entry.Type,
			endValue.SourceCommandID,
			current.Attempt.SourceCommandID,
		); err != nil {
			return err
		}
		if !sameTurn(endValue.Turn, current.Attempt.Turn) {
			return fmt.Errorf(
				"compaction/end owner %s does not match compaction/start owner %s",
				turnLabel(endValue.Turn),
				turnLabel(current.Attempt.Turn),
			)
		}
		if err := validateCompactionOwner(
			current.Attempt.Turn,
			current.OpenTurn,
			entry.Type,
		); err != nil {
			return err
		}
		if endValue.Error == nil && !current.Attempt.Summarized {
			return errors.New("successful compaction/end requires one compaction/summary")
		}
		current.Attempt = nil
	case PruneEventName:
		_, err := DecodePrune(entry.Data)
		return err
	}
	return nil
}

func validateCompactionCheckpoint(current LogState, entry session.Event) error {
	messageValue, err := session.DeriveEventMessage(entry)
	if err != nil {
		return err
	}
	if messageValue == nil {
		return nil
	}
	origin, recognized, err := decodeCheckpointSource(messageValue.SourceValue())
	if err != nil {
		return err
	}
	if !recognized {
		return nil
	}
	if current.Attempt == nil {
		return errors.New("compaction checkpoint has no matching compaction/start")
	}
	if origin.CompactionID != current.Attempt.CompactionID {
		return fmt.Errorf(
			"compaction checkpoint id %s does not match compaction/start id %s",
			origin.CompactionID,
			current.Attempt.CompactionID,
		)
	}
	return validateSourceCommandOwner(
		"compaction checkpoint",
		origin.SourceCommandID,
		current.Attempt.SourceCommandID,
	)
}

func validateCompactionOwner(owner *int64, openTurn *int64, eventType string) error {
	if owner == nil {
		if openTurn != nil {
			return fmt.Errorf("%s is standalone but turn %d is open", eventType, *openTurn)
		}
		return nil
	}
	if openTurn == nil {
		return fmt.Errorf("%s for turn %d appended outside any open turn", eventType, *owner)
	}
	if *owner != *openTurn {
		return fmt.Errorf("%s names turn %d but open turn is %d", eventType, *owner, *openTurn)
	}
	return nil
}

func validateSourceCommandOwner(
	eventType string,
	actual *string,
	expected *string,
) error {
	if sameOptionalString(actual, expected) {
		return nil
	}
	return fmt.Errorf(
		"%s sourceCommandId %s does not match compaction/start sourceCommandId %s",
		eventType,
		optionalStringLabel(actual),
		optionalStringLabel(expected),
	)
}

func applyCompactionTurnBoundary(current *LogState, entry session.Event) error {
	if entry.Type != session.TurnStartEventName && entry.Type != session.TurnEndEventName {
		return nil
	}
	turnValue, err := decodeTurnBoundary(entry)
	if err != nil {
		return err
	}
	if entry.Type == session.TurnStartEventName {
		current.OpenTurn = &turnValue
	} else {
		current.OpenTurn = nil
	}
	return nil
}

func decodeTurnBoundary(entry session.Event) (int64, error) {
	var wireValue struct {
		Turn json.RawMessage `json:"turn"`
	}
	if err := json.Unmarshal(entry.Data, &wireValue); err != nil {
		return 0, fmt.Errorf("decode %s: %w", entry.Type, err)
	}
	return decodeRequiredPositiveTurn(wireValue.Turn, entry.Type)
}

func decodeRequiredPositiveTurn(rawValue json.RawMessage, label string) (int64, error) {
	if len(rawValue) == 0 {
		return 0, fmt.Errorf("%s turn is required", label)
	}
	var value int64
	if err := json.Unmarshal(rawValue, &value); err != nil || value <= 0 || value > maxSafeInteger {
		return 0, fmt.Errorf("%s turn must be a positive safe integer", label)
	}
	return value, nil
}

func sameTurn(left *int64, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func sameOptionalString(left *string, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func optionalStringLabel(value *string) string {
	if value == nil {
		return "undefined"
	}
	return *value
}

func turnLabel(value *int64) string {
	if value == nil {
		return "null"
	}
	return fmt.Sprintf("%d", *value)
}

func cloneTurn(source *int64) *int64 {
	if source == nil {
		return nil
	}
	detached := *source
	return &detached
}

func cloneLogState(source LogState) LogState {
	detached := LogState{
		OpenTurn: cloneTurn(source.OpenTurn),
	}
	if source.Attempt != nil {
		detached.Attempt = &OpenAttempt{
			CompactionID:    source.Attempt.CompactionID,
			SourceCommandID: cloneString(source.Attempt.SourceCommandID),
			StartSeq:        source.Attempt.StartSeq,
			Turn:            cloneTurn(source.Attempt.Turn),
			Summarized:      source.Attempt.Summarized,
		}
	}
	return detached
}

func inspectError(entry session.Event, cause error) error {
	return fmt.Errorf("compaction: inspect %s at seq %d: %w", entry.Type, entry.Seq, cause)
}
