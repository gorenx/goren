//go:build contract

package fixture

import (
	"encoding/json"
	"os"
	"testing"
)

// SourceCommit reads one feature-local contract source baseline.
func SourceCommit(testingContext testing.TB, baselinePath string) string {
	testingContext.Helper()
	rawValue, readErr := os.ReadFile(baselinePath)
	if readErr != nil {
		testingContext.Fatal(readErr)
	}
	var baseline struct {
		SchemaVersion int `json:"schemaVersion"`
		Source        struct {
			Commit string `json:"commit"`
		} `json:"source"`
	}
	if decodeErr := json.Unmarshal(rawValue, &baseline); decodeErr != nil {
		testingContext.Fatal(decodeErr)
	}
	if baseline.SchemaVersion != 1 || baseline.Source.Commit == "" {
		testingContext.Fatalf("invalid source baseline %q", baselinePath)
	}
	return baseline.Source.Commit
}
