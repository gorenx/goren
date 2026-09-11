package commands

import (
	"errors"
	"fmt"
	"github.com/gorenx/goren/session"
	"strings"
)

const maxSafeInteger int64 = 1<<53 - 1

func validateResult(outcome Result) error {
	switch outcome.Kind {
	case ResultSuccess:
		if outcome.SourceEventSeq != nil &&
			(*outcome.SourceEventSeq < 0 || *outcome.SourceEventSeq > maxSafeInteger) {
			return errors.New("commands: success sourceEventSeq must be a non-negative safe integer")
		}
		return nil
	case ResultError:
		if outcome.Text == nil || strings.TrimSpace(*outcome.Text) == "" {
			return errors.New("commands: error result text must not be empty")
		}
		if outcome.SourceEventSeq != nil {
			return errors.New("commands: error result cannot cite sourceEventSeq")
		}
		return nil
	default:
		return fmt.Errorf("commands: unknown result kind %q", outcome.Kind)
	}
}

func validateSourceEvent(events []session.Event, outcome Result) error {
	if outcome.SourceEventSeq == nil {
		return nil
	}
	for _, entry := range events {
		if entry.Seq != *outcome.SourceEventSeq {
			continue
		}
		if entry.Type == RunEventName || entry.Type == DoneEventName {
			return fmt.Errorf(
				"commands: sourceEventSeq %d identifies a command lifecycle event",
				*outcome.SourceEventSeq,
			)
		}
		return nil
	}
	return fmt.Errorf(
		"commands: sourceEventSeq %d does not identify a prior Session event",
		*outcome.SourceEventSeq,
	)
}
