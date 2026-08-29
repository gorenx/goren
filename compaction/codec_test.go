package compaction

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/llm"
)

func TestCompactionEventCodecsPreserveMergeExtension(t *testing.T) {
	t.Parallel()
	startValue, err := DecodeStart(json.RawMessage(
		`{"compactionId":"compact-1","turn":null}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	if startValue.CompactionID != "compact-1" || startValue.Turn != nil {
		t.Fatalf("decoded start = %#v", startValue)
	}

	usageValue := llm.TokenUsage{
		InputTokens:  7,
		OutputTokens: 3,
	}
	summaryValue := Summary{
		CompactionID:  "compact-1",
		Summary:       []agentmessage.ContentBlock{agentmessage.NewTextBlock("checkpoint")},
		RawOutput:     []agentmessage.ContentBlock{},
		LLMStreamCall: true,
		ShadowedRange: SurfaceRange{
			Start: 9,
			End:   2,
		},
		ShadowedSeqs:       []int64{9, 2},
		ShadowedTokenCount: 11,
		Provider:           "mock",
		Model:              "model-a",
		Usage:              &usageValue,
	}
	encoded, err := json.Marshal(summaryValue)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"rawOutput":[]`)) ||
		!bytes.Contains(encoded, []byte(`"llmStreamCall":true`)) {
		t.Fatalf("encoded summary = %s", encoded)
	}
	decoded, err := DecodeSummary(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !decoded.LLMStreamCall || decoded.RawOutput == nil ||
		len(decoded.RawOutput) != 0 || decoded.ShadowedRange.Start != 9 ||
		decoded.ShadowedRange.End != 2 {
		t.Fatalf("decoded summary = %#v", decoded)
	}

	unmarked := summaryValue
	unmarked.LLMStreamCall = false
	unmarked.RawOutput = []agentmessage.ContentBlock{}
	encoded, err = json.Marshal(unmarked)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"rawOutput":[]`)) ||
		bytes.Contains(encoded, []byte(`"llmStreamCall"`)) {
		t.Fatalf("encoded unmarked summary = %s", encoded)
	}
}

func TestCompactionEventCodecsRejectMalformedPayloads(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name       string
		rawValue   string
		decodeFunc func(json.RawMessage) error
		wantError  string
	}{
		{
			name:     "start unknown field",
			rawValue: `{"compactionId":"c","turn":null,"extra":true}`,
			decodeFunc: func(rawValue json.RawMessage) error {
				_, err := DecodeStart(rawValue)
				return err
			},
			wantError: "unknown field",
		},
		{
			name:     "start missing turn",
			rawValue: `{"compactionId":"c"}`,
			decodeFunc: func(rawValue json.RawMessage) error {
				_, err := DecodeStart(rawValue)
				return err
			},
			wantError: "turn is required",
		},
		{
			name:     "start null optional owner",
			rawValue: `{"compactionId":"c","sourceCommandId":null,"turn":1}`,
			decodeFunc: func(rawValue json.RawMessage) error {
				_, err := DecodeStart(rawValue)
				return err
			},
			wantError: "cannot be null",
		},
		{
			name: "summary null content",
			rawValue: `{"compactionId":"c","summary":null,"shadowedRange":{"start":1,"end":1},` +
				`"shadowedSeqs":[1],"shadowedTokenCount":0,"provider":"p","model":"m"}`,
			decodeFunc: func(rawValue json.RawMessage) error {
				_, err := DecodeSummary(rawValue)
				return err
			},
			wantError: "summary must be an array",
		},
		{
			name: "summary false stream discriminant",
			rawValue: `{"compactionId":"c","summary":[],"rawOutput":[],"llmStreamCall":false,` +
				`"shadowedRange":{"start":1,"end":1},"shadowedSeqs":[1],` +
				`"shadowedTokenCount":0,"provider":"p","model":"m"}`,
			decodeFunc: func(rawValue json.RawMessage) error {
				_, err := DecodeSummary(rawValue)
				return err
			},
			wantError: "must be true when present",
		},
		{
			name: "summary marked without raw output",
			rawValue: `{"compactionId":"c","summary":[],"llmStreamCall":true,` +
				`"shadowedRange":{"start":1,"end":1},"shadowedSeqs":[1],` +
				`"shadowedTokenCount":0,"provider":"p","model":"m"}`,
			decodeFunc: func(rawValue json.RawMessage) error {
				_, err := DecodeSummary(rawValue)
				return err
			},
			wantError: "requires rawOutput",
		},
		{
			name: "summary missing shadow count",
			rawValue: `{"compactionId":"c","summary":[],"shadowedRange":{"start":1,"end":1},` +
				`"shadowedSeqs":[1],"provider":"p","model":"m"}`,
			decodeFunc: func(rawValue json.RawMessage) error {
				_, err := DecodeSummary(rawValue)
				return err
			},
			wantError: "shadowedTokenCount is required",
		},
		{
			name: "summary usage missing output",
			rawValue: `{"compactionId":"c","summary":[],"shadowedRange":{"start":1,"end":1},` +
				`"shadowedSeqs":[1],"shadowedTokenCount":0,"provider":"p","model":"m",` +
				`"usage":{"inputTokens":1}}`,
			decodeFunc: func(rawValue json.RawMessage) error {
				_, err := DecodeSummary(rawValue)
				return err
			},
			wantError: "outputTokens is required",
		},
		{
			name:     "end missing turn",
			rawValue: `{"compactionId":"c"}`,
			decodeFunc: func(rawValue json.RawMessage) error {
				_, err := DecodeEnd(rawValue)
				return err
			},
			wantError: "turn is required",
		},
		{
			name: "prune mismatched endpoints",
			rawValue: `{"shadowedRange":{"start":1,"end":2},` +
				`"shadowedSeqs":[1,3],"shadowedTokenCount":4}`,
			decodeFunc: func(rawValue json.RawMessage) error {
				_, err := DecodePrune(rawValue)
				return err
			},
			wantError: "first and last",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			err := testCase.decodeFunc(json.RawMessage(testCase.rawValue))
			if err == nil || !strings.Contains(err.Error(), testCase.wantError) {
				t.Fatalf("decode error = %v, want %q", err, testCase.wantError)
			}
		})
	}
}

func TestDecodePruneAcceptsSurfaceOrderInsteadOfNumericOrder(t *testing.T) {
	t.Parallel()
	decoded, err := DecodePrune(json.RawMessage(
		`{"shadowedRange":{"start":9,"end":2},` +
			`"shadowedSeqs":[9,7,2],"shadowedTokenCount":4}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.ShadowedRange.Start != 9 || decoded.ShadowedRange.End != 2 ||
		len(decoded.ShadowedSeqs) != 3 {
		t.Fatalf("decoded prune = %#v", decoded)
	}
}
