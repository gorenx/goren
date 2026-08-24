// Package command implements the human-facing /compact Consumer over the
// backend-independent Commands and Compaction seams.
package command

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/gorenx/goren/commands"
	"github.com/gorenx/goren/compaction"
)

const usage = "Usage: /compact (no arguments)"

// Compact is the /compact business operation. Plugin owns registration and
// lifecycle; this object owns argument validation and result translation.
type Compact struct {
	compactor compaction.Engine
}

func (operation *Compact) bind(compactor compaction.Engine) {
	operation.compactor = compactor
}

func (operation *Compact) release() {
	operation.compactor = nil
}

// Execute runs one argument-free manual compaction request.
func (operation *Compact) Execute(
	requestContext context.Context,
	input commands.Invocation,
) (commands.Result, error) {
	if strings.TrimSpace(input.RawInput) != "" {
		return errorResult(usage), nil
	}
	if operation.compactor == nil {
		return commands.Result{}, errors.New("command-compact: Compaction Engine is unavailable")
	}
	identifier := string(input.CommandID)
	outcome, err := operation.compactor.CompactNow(
		requestContext,
		input.Agent,
		&identifier,
	)
	if err != nil {
		if requestContext.Err() != nil {
			return errorResult("Compaction cancelled."), nil
		}
		var expected *compaction.ManualError
		if errors.As(err, &expected) {
			return expectedFailure(expected.Code)
		}
		return commands.Result{}, err
	}
	if outcome == nil {
		return successResult("No compactable history yet."), nil
	}
	message := fmt.Sprintf(
		"Compacted %d history items (~%d tokens).",
		len(outcome.ShadowedSeqs),
		outcome.ShadowedTokenCount,
	)
	return commands.Result{
		Kind:           commands.ResultSuccess,
		Text:           &message,
		SourceEventSeq: &outcome.SummarySeq,
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
