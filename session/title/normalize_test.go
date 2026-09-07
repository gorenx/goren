package title

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestNormalizeAndFallbackSessionTitle(t *testing.T) {
	t.Parallel()
	normalized, err := NormalizeSessionTitle("\x1b]0;hidden\x07  Hello\t world \u202e", 80)
	if err != nil {
		t.Fatal(err)
	}
	if normalized != "Hello world" {
		t.Fatalf("normalized title = %q", normalized)
	}
	fallback, err := FallbackSessionTitle("one two three four", 3, 9)
	if err != nil {
		t.Fatal(err)
	}
	if fallback != "one two t" {
		t.Fatalf("fallback title = %q", fallback)
	}
	bounded, err := TruncateTitleUTF8("你好世界", 7)
	if err != nil {
		t.Fatal(err)
	}
	if bounded != "你好" {
		t.Fatalf("UTF-8 bounded title = %q", bounded)
	}
}

func TestEventDataKeepsUserMessageSeqsAsJSONArray(t *testing.T) {
	t.Parallel()
	wireValue, err := json.Marshal(
		EventData{
			Title:       "Named",
			MessageSeqs: []int64{},
			Source: UserSource{
				Kind: "user",
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(wireValue, []byte(`"messageSeqs":[]`)) {
		t.Fatalf("user title event = %s, want an empty JSON array", wireValue)
	}
	var decoded EventData
	if err := json.Unmarshal(
		[]byte(`{"title":"Named","messageSeqs":null,"source":{"kind":"user"}}`),
		&decoded,
	); err == nil {
		t.Fatal("null messageSeqs was accepted")
	}
}
