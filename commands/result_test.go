package commands

import (
	"github.com/gorenx/goren/session"
	"testing"
)

func TestResultValidationAndSourceOwnership(t *testing.T) {
	blank := " "
	text := "error"
	negative := int64(-1)
	tooLarge := maxSafeInteger + 1
	source := int64(0)
	for _, input := range []Result{
		{
			Kind: "unknown",
		},
		{
			Kind:           ResultSuccess,
			SourceEventSeq: &negative,
		},
		{
			Kind:           ResultSuccess,
			SourceEventSeq: &tooLarge,
		},
		{
			Kind: ResultError,
			Text: &blank,
		},
		{
			Kind:           ResultError,
			Text:           &text,
			SourceEventSeq: &source,
		},
	} {
		if err := validateResult(input); err == nil {
			t.Fatalf("accepted %#v", input)
		}
	}
	outcome := Result{
		Kind:           ResultSuccess,
		SourceEventSeq: &source,
	}
	if err := validateSourceEvent(nil, outcome); err == nil {
		t.Fatal("missing source accepted")
	}
	for _, kind := range []string{RunEventName, DoneEventName} {
		if err := validateSourceEvent([]session.Event{{
			Seq:  source,
			Type: kind,
		}}, outcome); err == nil {
			t.Fatal("command event accepted as source")
		}
	}
	if err := validateSourceEvent([]session.Event{{
		Seq:  source,
		Type: "compaction/summary",
	}}, outcome); err != nil {
		t.Fatal(err)
	}
}
