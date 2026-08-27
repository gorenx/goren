package agentmessage_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/gorenx/goren/agentmessage"
)

// TestPinnedHarnessMessageFixtures locks the observable Message vocabulary
// from packages/llm/llm/src/message.ts and types.ts at
// 47f943859bef60e4160492346772ded9b24f765a.
func TestPinnedHarnessMessageFixtures(t *testing.T) {
	t.Parallel()
	fixtureFile, err := os.Open("testdata/pinned-message-fixtures.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := fixtureFile.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	}()

	scanner := bufio.NewScanner(fixtureFile)
	fixtureIndex := 0
	for scanner.Scan() {
		fixtureIndex++
		rawValue := append(json.RawMessage(nil), scanner.Bytes()...)
		entry, decodeErr := agentmessage.DecodeMessage(rawValue)
		if decodeErr != nil {
			t.Fatalf("fixture %d decode: %v", fixtureIndex, decodeErr)
		}
		encoded, encodeErr := json.Marshal(entry)
		if encodeErr != nil {
			t.Fatalf("fixture %d encode: %v", fixtureIndex, encodeErr)
		}
		if !bytes.Equal(encoded, rawValue) {
			t.Fatalf("fixture %d round trip = %s, want %s", fixtureIndex, encoded, rawValue)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if fixtureIndex != 5 {
		t.Fatalf("fixture count = %d, want 5", fixtureIndex)
	}
}
