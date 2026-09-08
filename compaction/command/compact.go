// Package command implements the human-facing /compact Consumer over the
// backend-independent Commands and Compaction seams.
package command

import (
	"errors"
	"fmt"
	"strings"

	"github.com/gorenx/goren/commands"
	"github.com/gorenx/goren/compaction"
)

// Definition supplies the canonical metadata registered by composition.
func Definition() commands.Definition {
	return commands.Definition{
		Name:        "compact",
		Description: "Compact older conversation history",
	}
}

const usage = "Usage: /compact (no arguments)"

// ErrArguments reports the command's argument-free input contract.
var ErrArguments = errors.New(usage)

// ValidateInput rejects arguments before the runtime starts a compaction.
func ValidateInput(rawInput string) error {
	if strings.TrimSpace(rawInput) != "" {
		return ErrArguments
	}
	return nil
}

// RenderResult maps a settled Compaction result to the direct command response.
// It does not invoke the provider, own cancellation or record command events.
func RenderResult(outcome *compaction.Result, failure error) (commands.Result, error) {
	if failure != nil {
		if errors.Is(failure, ErrArguments) {
			return errorResult(usage), nil
		}
		var expected *compaction.ManualError
		if errors.As(failure, &expected) {
			return expectedFailure(expected.Code)
		}
		return commands.Result{}, failure
	}
	if outcome == nil {
		return successResult("No compactable history yet."), nil
	}
	message := fmt.Sprintf(
		"Compacted %d history items (~%d tokens).",
		len(outcome.ShadowedSeqs),
		outcome.ShadowedTokenCount,
	)
	summarySeq := outcome.SummarySeq
	return commands.Result{
		Kind:           commands.ResultSuccess,
		Text:           &message,
		SourceEventSeq: &summarySeq,
	}, nil
}

func expectedFailure(code compaction.ManualErrorCode) (commands.Result, error) {
	switch code {
	case compaction.ManualErrorBusy:
		return errorResult(
			"Compaction is unavailable because this process has an active compaction, or the agent is not idle.",
		), nil
	case compaction.ManualErrorCancelled:
		return errorResult("Compaction cancelled."), nil
	case compaction.ManualErrorChanged:
		return errorResult(
			"The history selected for compaction changed before it could be replaced. The conversation is unchanged; the attempt is recorded in the session log.",
		), nil
	case compaction.ManualErrorSummary:
		return errorResult(
			"Compaction could not produce a useful summary. The conversation is unchanged; the attempt is recorded in the session log.",
		), nil
	case compaction.ManualErrorCommit:
		return errorResult(
			"Compaction did not finish cleanly; some session history may have changed. Inspect the current session state before retrying.",
		), nil
	case compaction.ManualErrorPersistence:
		return errorResult("Compaction finished, but the session could not be saved."), nil
	default:
		return commands.Result{}, fmt.Errorf(
			"command-compact: unknown manual compaction error code %q",
			code,
		)
	}
}

func successResult(message string) commands.Result {
	return commands.Result{
		Kind: commands.ResultSuccess,
		Text: &message,
	}
}

func errorResult(message string) commands.Result {
	return commands.Result{
		Kind: commands.ResultError,
		Text: &message,
	}
}
