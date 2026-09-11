package delegation

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

func TestNumericDepthLimitRejectsUnsafeValues(t *testing.T) {
	t.Parallel()
	if _, numericErr := NewNumericDepthLimit(-1); numericErr == nil {
		t.Fatal("negative maxDepth was accepted")
	}
}

func TestConfigResolveValidatesAndDetachesRuntimeSettings(t *testing.T) {
	configured := DefaultConfig()
	if _, err := configured.Resolve(); err == nil {
		t.Fatal("missing provider accepted without creating a Plugin")
	}
	configured.Provider = "spawn"
	personaText := "original"
	limit := 100
	allowed := []string{"read"}
	configured.Persona = &personaText
	configured.AgentOptions = &AgentOptions{
		MaxTokens: &limit,
	}
	configured.ToolFilter = &ToolFilter{
		Allow: &allowed,
	}
	resolved, err := configured.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	personaText = "changed"
	limit = 200
	allowed[0] = "write"
	if *resolved.Persona != "original" || *resolved.AgentOptions.MaxTokens != 100 || resolved.ToolFilter.Allow[0] != "read" {
		t.Fatal("runtime settings retained mutable configuration input")
	}
	if resolved.MaxDepth == nil || *resolved.MaxDepth != 3 || !resolved.EnableRunInBackground || resolved.BackgroundMode != BackgroundOneShot {
		t.Fatal("deployment defaults changed")
	}
	configured.BackgroundMode = "unsupported"
	if _, err := configured.Resolve(); err == nil {
		t.Fatal("invalid background mode accepted")
	}
}
