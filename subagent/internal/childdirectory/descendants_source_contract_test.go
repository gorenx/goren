//go:build contract

package childdirectory

import (
	"context"
	"reflect"
	"testing"

	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

func TestPinnedSourceDescendantTraversalMatchesGo(t *testing.T) {
	rootID := session.SessionID("catalog-tree-root")
	branchA := newContractDirectorySession(
		t,
		"branch-a",
		rootID,
		1,
		session.OriginSubagent,
		subagent.ContinuableDescriptor{
			Provider: "spawn",
			Label:    "branch a",
		},
	)
	ordinary := newContractDirectorySession(t, "ordinary-middle", branchA.ID(), 2, "")
	oneShotLabel := "one shot"
	oneShot := newContractDirectorySession(
		t,
		"one-shot-middle",
		ordinary.ID(),
		3,
		session.OriginSubagent,
		subagent.OneShotDescriptor{
			Provider: "spawn",
			Label:    &oneShotLabel,
		},
	)
	creationWindow := newContractDirectorySession(
		t,
		"creation-window-middle",
		oneShot.ID(),
		4,
		session.OriginSubagent,
	)
	deepLeaf := newContractDirectorySession(
		t,
		"deep-leaf",
		creationWindow.ID(),
		5,
		session.OriginSubagent,
		subagent.ContinuableDescriptor{
			Provider: "spawn",
			Label:    "deep leaf",
		},
	)
	branchB := newContractDirectorySession(
		t,
		"branch-b",
		rootID,
		6,
		session.OriginSubagent,
		subagent.ContinuableDescriptor{
			Provider: "spawn",
			Label:    "branch b",
		},
	)

	directory := New()
	if enableErr := directory.Enable(
		sessionList{
			conversations: []session.Context{
				branchA,
				ordinary,
				oneShot,
				creationWindow,
				deepLeaf,
				branchB,
			},
		},
		nil,
		projectionRegistry(t),
		nil,
	); enableErr != nil {
		t.Fatal(enableErr)
	}
	entries, listErr := directory.ListDescendants(context.Background(), rootID)
	if listErr != nil {
		t.Fatal(listErr)
	}
	goOutput := directoryContractJSON(t, directoryContractDescendants(entries))
	sourceOutput := sourceDirectoryJSON(
		t,
		directorySourceOutput(t, "subagent-catalog-descendants.ts"),
	)
	if !reflect.DeepEqual(goOutput, sourceOutput) {
		t.Fatalf("Go entries = %#v, source entries = %#v", goOutput, sourceOutput)
	}
}
