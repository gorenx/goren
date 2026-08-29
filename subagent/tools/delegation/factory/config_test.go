package factory

import (
	"encoding/json"
	"testing"
)

func TestDepthLimitPreservesBothConfigurationVariants(t *testing.T) {
	t.Parallel()
	numeric, numericErr := NewNumericDepthLimit(3)
	if numericErr != nil {
		t.Fatal(numericErr)
	}
	numericJSON, encodeErr := json.Marshal(numeric)
	if encodeErr != nil {
		t.Fatal(encodeErr)
	}
	if string(numericJSON) != "3" {
		t.Fatalf("numeric maxDepth = %s", numericJSON)
	}
	var unspecified DepthLimit
	if decodeErr := json.Unmarshal(
		[]byte(`"provider-managed"`),
		&unspecified,
	); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	unspecifiedJSON, encodeErr := json.Marshal(unspecified)
	if encodeErr != nil {
		t.Fatal(encodeErr)
	}
	if string(unspecifiedJSON) != `"provider-managed"` {
		t.Fatalf("unspecified maxDepth = %s", unspecifiedJSON)
	}
}

func TestDecodeConfigMapsUnspecifiedDepthToNoLocalLimit(t *testing.T) {
	t.Parallel()
	settings, decodeErr := decodeConfig(json.RawMessage(`{
  "provider": "spawn",
  "maxDepth": "provider-managed"
}`))
	if decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if settings.MaxDepth != nil {
		t.Fatalf("provider-managed maxDepth = %d", *settings.MaxDepth)
	}
}

func TestNumericDepthLimitRejectsUnsafeValues(t *testing.T) {
	t.Parallel()
	if _, numericErr := NewNumericDepthLimit(-1); numericErr == nil {
		t.Fatal("negative maxDepth was accepted")
	}
}
