package llm

import "testing"

func TestConfigurationSnapshotsAndRejectedReplacement(t *testing.T) {
	var settings Configuration
	limit := 20
	proposal := CallConfig{
		Provider:  "p",
		Model:     "m",
		MaxTokens: &limit,
		Stop:      []string{"end"},
	}
	if err := settings.Set("a", proposal); err != nil {
		t.Fatal(err)
	}
	limit = 40
	proposal.Stop[0] = "changed"
	captured, err := settings.Get("a")
	if err != nil || *captured.MaxTokens != 20 || captured.Stop[0] != "end" {
		t.Fatalf("configuration aliases submitted data: %+v, %v", captured, err)
	}
	*captured.MaxTokens = 60
	captured.Stop[0] = "changed again"
	if err := settings.Set("a", CallConfig{}); err == nil {
		t.Fatal("invalid replacement accepted")
	}
	stored, err := settings.Get("a")
	if err != nil || *stored.MaxTokens != 20 || stored.Stop[0] != "end" {
		t.Fatalf("rejected replacement or read mutated settings: %+v, %v", stored, err)
	}
	if _, err := settings.Get("b"); err == nil {
		t.Fatal("unregistered Agent borrowed configuration")
	}
	settings.Remove("a")
	settings.Remove("a")
	if _, err := settings.Get("a"); err == nil {
		t.Fatal("removed configuration remains readable")
	}
	if *stored.MaxTokens != 20 {
		t.Fatal("removing configuration changed a captured call")
	}
}
