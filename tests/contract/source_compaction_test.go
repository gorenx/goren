//go:build contract

package contract_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPinnedCompactionSourceGeneratesCommittedVectors(t *testing.T) {
	repositoryRoot, sourceRoot := contractPaths(t)
	commandContext, cancel := context.WithTimeout(
		context.Background(),
		30*time.Second,
	)
	defer cancel()
	output, err := runTypeScript(
		commandContext,
		sourceRoot,
		filepath.Join(
			repositoryRoot,
			"tests",
			"contract",
			"typescript",
			"generate-compaction-vectors.ts",
		),
		sourceRoot,
	)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join(
		repositoryRoot,
		"contracts",
		"deepseek-harness",
		"compaction-vectors.json",
	))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(output, want) {
		t.Fatal("pinned source no longer generates compaction-vectors.json")
	}
}
