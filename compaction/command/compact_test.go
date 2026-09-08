package command

import (
	"errors"
	"github.com/gorenx/goren/commands"
	"github.com/gorenx/goren/compaction"
	"testing"
)

func TestCompactMapsEveryExpectedManualFailure(t *testing.T) {
	testCases := []struct {
		code compaction.ManualErrorCode
		text string
	}{
		{
			code: compaction.ManualErrorBusy,
			text: "Compaction is unavailable because this process has an active compaction, or the agent is not idle.",
		},
		{
			code: compaction.ManualErrorCancelled,
			text: "Compaction cancelled.",
		},
		{
			code: compaction.ManualErrorChanged,
			text: "The history selected for compaction changed before it could be replaced. The conversation is unchanged; the attempt is recorded in the session log.",
		},
		{
			code: compaction.ManualErrorSummary,
			text: "Compaction could not produce a useful summary. The conversation is unchanged; the attempt is recorded in the session log.",
		},
		{
			code: compaction.ManualErrorCommit,
			text: "Compaction did not finish cleanly; some session history may have changed. Inspect the current session state before retrying.",
		},
		{
			code: compaction.ManualErrorPersistence,
			text: "Compaction finished, but the session could not be saved.",
		},
	}
	for _, testCase := range testCases {
		t.Run(string(testCase.code), func(t *testing.T) {
			outcome, err := RenderResult(nil, &compaction.ManualError{
				Code:    testCase.code,
				Message: "provider detail",
			})
			if err != nil || outcome.Kind != commands.ResultError || outcome.Text == nil || *outcome.Text != testCase.text {
				t.Fatalf("result: %#v, %v", outcome, err)
			}
		})
	}
}

func TestCompactInputAndResultMapping(t *testing.T) {
	for _, raw := range []string{"", " ", "\n\t"} {
		if err := ValidateInput(raw); err != nil {
			t.Fatal(err)
		}
	}
	if err := ValidateInput(" now"); !errors.Is(err, ErrArguments) {
		t.Fatalf("arguments: %v", err)
	}
	rejected, err := RenderResult(nil, ErrArguments)
	if err != nil || rejected.Kind != commands.ResultError || rejected.Text == nil || *rejected.Text != usage {
		t.Fatalf("usage: %#v, %v", rejected, err)
	}
	empty, err := RenderResult(nil, nil)
	if err != nil || empty.Kind != commands.ResultSuccess || empty.Text == nil || *empty.Text != "No compactable history yet." {
		t.Fatalf("empty: %#v, %v", empty, err)
	}
	input := &compaction.Result{
		SummarySeq:         8,
		ShadowedSeqs:       []int64{1, 3, 7},
		ShadowedTokenCount: 42,
	}
	outcome, err := RenderResult(input, nil)
	if err != nil || outcome.SourceEventSeq == nil || *outcome.SourceEventSeq != 8 || outcome.Text == nil || *outcome.Text != "Compacted 3 history items (~42 tokens)." {
		t.Fatalf("success: %#v, %v", outcome, err)
	}
	input.SummarySeq = 99
	if *outcome.SourceEventSeq != 8 {
		t.Fatal("response aliases provider result")
	}
	unexpected := errors.New("unexpected failure")
	if _, err := RenderResult(nil, unexpected); !errors.Is(err, unexpected) {
		t.Fatalf("unexpected: %v", err)
	}
	if _, err := RenderResult(nil, &compaction.ManualError{
		Code: "unknown",
	}); err == nil {
		t.Fatal("unknown classification hidden")
	}
}
