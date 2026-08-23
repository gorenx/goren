//go:build contract

package catalog

import (
	"context"
	"reflect"
	"testing"

	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

func TestPinnedSourceDescendantTraversalMatchesGo(t *testing.T) {
	rootID := session.SessionID("catalog-tree-root")
	branchA := newContractCatalogSession(
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
	ordinary := newContractCatalogSession(t, "ordinary-middle", branchA.ID(), 2, "")
	oneShotLabel := "one shot"
	oneShot := newContractCatalogSession(
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
	creationWindow := newContractCatalogSession(
		t,
		"creation-window-middle",
		oneShot.ID(),
		4,
		session.OriginSubagent,
	)
	deepLeaf := newContractCatalogSession(
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
	branchB := newContractCatalogSession(
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

	catalogService := New()
	if enableErr := catalogService.Enable(
		sessionList{
			conversations: []*session.Session{
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
	); enableErr != nil {
		t.Fatal(enableErr)
	}
	entries, listErr := catalogService.ListDescendants(context.Background(), rootID)
	if listErr != nil {
		t.Fatal(listErr)
	}
	goOutput := catalogContractJSON(t, catalogContractDescendants(entries))
	sourceOutput := sourceCatalogJSON(
		t,
		catalogSourceOutput(t, "subagent-catalog-descendants.ts"),
	)
	if !reflect.DeepEqual(goOutput, sourceOutput) {
		t.Fatalf("Go entries = %#v, source entries = %#v", goOutput, sourceOutput)
	}
}
