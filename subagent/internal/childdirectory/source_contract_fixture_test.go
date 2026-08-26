//go:build contract

package childdirectory

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	contractfixture "github.com/gorenx/goren/tests/contract/fixture"
)

func directorySourceOutput(t *testing.T, scriptName string) []byte {
	t.Helper()
	repositoryRoot, sourceRoot := contractfixture.Paths(t)
	sourceCommit := contractfixture.SourceCommit(
		t,
		filepath.Join(
			repositoryRoot,
			"subagent",
			"testdata",
			"source-baseline.json",
		),
	)
	requestContext, cancelRequest := context.WithTimeout(
		context.Background(),
		30*time.Second,
	)
	defer cancelRequest()
	sourceOutput, sourceErr := contractfixture.RunTypeScript(
		requestContext,
		sourceRoot,
		nil,
		filepath.Join(
			repositoryRoot,
			"tests",
			"contract",
			"typescript",
			scriptName,
		),
		sourceRoot,
		sourceCommit,
	)
	if sourceErr != nil {
		t.Fatal(sourceErr)
	}
	return sourceOutput
}

func directoryContractJSON(t *testing.T, entries any) any {
	t.Helper()
	rawValue, encodeErr := json.Marshal(entries)
	if encodeErr != nil {
		t.Fatal(encodeErr)
	}
	var decoded any
	if decodeErr := json.Unmarshal(rawValue, &decoded); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	return decoded
}

func sourceDirectoryJSON(t *testing.T, rawValue []byte) any {
	t.Helper()
	var decoded any
	if decodeErr := json.Unmarshal(rawValue, &decoded); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	return decoded
}

func directoryContractEntries(entries []subagent.ChildEntry) []map[string]any {
	result := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		result = append(result, directoryContractEntry(entry))
	}
	return result
}

func directoryContractDescendants(
	entries []subagent.DescendantEntry,
) []map[string]any {
	result := make([]map[string]any, 0, len(entries))
	for _, descendantEntry := range entries {
		entry := directoryContractEntry(descendantEntry.Entry)
		entry["parentId"] = string(descendantEntry.ParentID)
		entry["depth"] = descendantEntry.Depth
		result = append(result, entry)
	}
	return result
}

func directoryContractEntry(entry subagent.ChildEntry) map[string]any {
	switch value := entry.(type) {
	case subagent.OneShotChildEntry:
		result := map[string]any{
			"kind":        "child",
			"id":          string(value.ID),
			"mode":        string(subagent.ModeOneShot),
			"activity":    string(value.Activity),
			"hasChildren": value.HasChildren,
		}
		if value.Label != nil {
			result["label"] = *value.Label
		}
		return result
	case subagent.ContinuableChildEntry:
		return map[string]any{
			"kind":        "child",
			"id":          string(value.ID),
			"mode":        string(subagent.ModeContinuable),
			"label":       value.Label,
			"activity":    string(value.Activity),
			"hasChildren": value.HasChildren,
		}
	case subagent.DiagnosticEntry:
		return map[string]any{
			"kind":   "diagnostic",
			"id":     string(value.ID),
			"reason": string(value.Reason),
		}
	default:
		return map[string]any{
			"kind": "unknown",
		}
	}
}

func newContractDirectorySession(
	t *testing.T,
	identifier session.SessionID,
	parentID session.SessionID,
	createdAt int64,
	origin session.Origin,
	descriptors ...subagent.Descriptor,
) session.Context {
	t.Helper()
	conversation, createErr := session.New(
		identifier,
		session.CreateOptions{
			Metadata: session.Metadata{
				CreatedAt:     int64Pointer(createdAt),
				ParentSession: sessionIDPointer(parentID),
				Origin:        origin,
			},
		},
	)
	if createErr != nil {
		t.Fatal(createErr)
	}
	for _, descriptor := range descriptors {
		if descriptor == nil {
			continue
		}
		descriptorData, snapshotErr := subagent.SnapshotDescriptor(descriptor)
		if snapshotErr != nil {
			t.Fatal(snapshotErr)
		}
		{
			var committedEvent session.Event
			var writeErr error
			draft, draftErr := session.NewEventDraft(subagent.DescriptorEvent,
				descriptorData)
			writeErr = draftErr
			if draftErr == nil {
				receipt, commitErr := conversation.Commit(context.Background(), session.Batch(draft))
				writeErr = commitErr
				if commitErr == nil {
					committedEvent = receipt.Events[0]
				}
			}
			if _, appendErr := committedEvent, writeErr; appendErr != nil {
				t.Fatal(appendErr)
			}
		}
	}
	return conversation
}
