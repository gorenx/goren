//go:build contract

package catalog

import (
	"context"
	"reflect"
	"testing"

	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

func TestPinnedSourceLiveChildrenMatchGo(t *testing.T) {
	rootID := session.SessionID("catalog-live-root")
	ordinary := newContractCatalogSession(t, "ordinary-child", rootID, 1, "")
	creationWindow := newContractCatalogSession(
		t,
		"creation-window",
		rootID,
		0,
		session.OriginSubagent,
	)
	tieB := newContractCatalogSession(
		t,
		"tie-b",
		rootID,
		5,
		session.OriginSubagent,
		subagent.ContinuableDescriptor{
			Provider: "spawn",
			Label:    "tie b",
		},
	)
	tieA := newContractCatalogSession(
		t,
		"tie-a",
		rootID,
		5,
		session.OriginSubagent,
		subagent.ContinuableDescriptor{
			Provider: "spawn",
			Label:    "tie a",
		},
	)
	grandchild := newContractCatalogSession(
		t,
		"grandchild",
		tieB.ID(),
		6,
		session.OriginSubagent,
		subagent.ContinuableDescriptor{
			Provider: "spawn",
			Label:    "nested",
		},
	)
	oneShotLabel := "terminal"
	lateOneShot := newContractCatalogSession(
		t,
		"late-one-shot",
		rootID,
		9,
		session.OriginSubagent,
		subagent.OneShotDescriptor{
			Provider: "spawn",
			Label:    &oneShotLabel,
		},
	)
	repeated := newContractCatalogSession(
		t,
		"repeated",
		rootID,
		10,
		session.OriginSubagent,
		subagent.ContinuableDescriptor{
			Provider: "spawn",
			Label:    "first",
		},
		subagent.ContinuableDescriptor{
			Provider: "spawn",
			Label:    "last",
		},
	)
	orderedIDs := []session.SessionID{
		"_tie",
		"-tie",
		"a-tie",
		"A-tie",
		"ä-tie",
		"B-tie",
		"child-10",
		"child-2",
		"e\u0301-tie",
		"é-tie",
	}
	ordered := make([]*session.Session, 0, len(orderedIDs))
	for _, identifier := range orderedIDs {
		ordered = append(
			ordered,
			newContractCatalogSession(
				t,
				identifier,
				rootID,
				11,
				session.OriginSubagent,
				subagent.ContinuableDescriptor{
					Provider: "spawn",
					Label:    string(identifier),
				},
			),
		)
	}
	conversations := []*session.Session{
		ordinary,
		creationWindow,
		tieB,
		tieA,
		grandchild,
		lateOneShot,
		repeated,
	}
	conversations = append(conversations, ordered...)

	catalogService := New()
	if enableErr := catalogService.Enable(
		sessionList{
			conversations: conversations,
		},
		nil,
		projectionRegistry(t),
	); enableErr != nil {
		t.Fatal(enableErr)
	}
	entries, listErr := catalogService.ListChildren(context.Background(), rootID)
	if listErr != nil {
		t.Fatal(listErr)
	}
	goOutput := catalogContractJSON(t, catalogContractEntries(entries))
	sourceOutput := sourceCatalogJSON(
		t,
		catalogSourceOutput(t, "subagent-catalog-live.ts"),
	)
	if !reflect.DeepEqual(goOutput, sourceOutput) {
		t.Fatalf("Go entries = %#v, source entries = %#v", goOutput, sourceOutput)
	}
}
