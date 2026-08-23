//go:build contract

package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/gorenx/goren/session"
	sessionpersistence "github.com/gorenx/goren/session/persistence"
	"github.com/gorenx/goren/subagent"
)

func TestPinnedSourceColdChildrenMatchGo(t *testing.T) {
	rootID := session.SessionID("catalog-cold-root")
	healthy := newContractCatalogSession(
		t,
		"cold-healthy",
		rootID,
		1,
		session.OriginSubagent,
		subagent.ContinuableDescriptor{
			Provider: "spawn",
			Label:    "healthy",
		},
	)
	oneShotLabel := "terminal"
	oneShot := newContractCatalogSession(
		t,
		"cold-one-shot",
		rootID,
		2,
		session.OriginSubagent,
		subagent.OneShotDescriptor{
			Provider: "spawn",
			Label:    &oneShotLabel,
		},
	)
	missing := newContractCatalogSession(
		t,
		"cold-missing",
		rootID,
		3,
		session.OriginSubagent,
	)
	invalid := newContractCatalogSession(
		t,
		"cold-invalid",
		rootID,
		4,
		session.OriginSubagent,
	)
	unavailable := newContractCatalogSession(
		t,
		"cold-unavailable",
		rootID,
		5,
		session.OriginSubagent,
	)
	replaced := newContractCatalogSession(
		t,
		"cold-replaced",
		rootID,
		6,
		session.OriginSubagent,
		subagent.ContinuableDescriptor{
			Provider: "spawn",
			Label:    "wrong lifecycle",
		},
	)
	repeated := newContractCatalogSession(
		t,
		"cold-repeated",
		rootID,
		7,
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
	staleLive := newContractCatalogSession(
		t,
		"live-preferred",
		"stale-parent",
		8,
		session.OriginSubagent,
	)
	live := newContractCatalogSession(
		t,
		"live-preferred",
		rootID,
		8,
		session.OriginSubagent,
		subagent.ContinuableDescriptor{
			Provider: "spawn",
			Label:    "live wins",
		},
	)
	grandchild := newContractCatalogSession(
		t,
		"cold-grandchild",
		healthy.ID(),
		9,
		session.OriginSubagent,
		subagent.ContinuableDescriptor{
			Provider: "spawn",
			Label:    "nested",
		},
	)
	invalidEvents := []session.Event{
		{
			Type: subagent.DescriptorEventName,
			Seq:  0,
			Time: 1,
			Data: json.RawMessage(
				`{"version":2,"mode":"continuable","provider":"spawn","label":"was valid"}`,
			),
		},
		{
			Type: subagent.DescriptorEventName,
			Seq:  1,
			Time: 2,
			Data: json.RawMessage(
				`{"version":2,"mode":"continuable","provider":7}`,
			),
		},
	}
	replacedInspection := inspectionOf(replaced)
	replacedInspection.Header.CreatedAt += 100
	durability := &persistenceView{
		headers: []session.Header{
			grandchild.Header(),
			staleLive.Header(),
			repeated.Header(),
			replaced.Header(),
			unavailable.Header(),
			invalid.Header(),
			missing.Header(),
			oneShot.Header(),
			healthy.Header(),
		},
		inspections: map[session.SessionID]sessionpersistence.Inspection{
			healthy.ID(): inspectionOf(healthy),
			oneShot.ID(): inspectionOf(oneShot),
			missing.ID(): inspectionOf(missing),
			invalid.ID(): {
				Header: invalid.Header(),
				Events: invalidEvents,
			},
			replaced.ID():   replacedInspection,
			repeated.ID():   inspectionOf(repeated),
			grandchild.ID(): inspectionOf(grandchild),
		},
		failures: map[session.SessionID]error{
			unavailable.ID(): errors.New("unavailable"),
		},
	}
	catalogService := New()
	if enableErr := catalogService.Enable(
		sessionList{
			conversations: []*session.Session{
				live,
			},
		},
		durability,
		projectionRegistry(t),
	); enableErr != nil {
		t.Fatal(enableErr)
	}
	entries, listErr := catalogService.ListChildren(context.Background(), rootID)
	if listErr != nil {
		t.Fatal(listErr)
	}
	cancelledContext, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()
	_, cancelledErr := catalogService.ListChildren(cancelledContext, rootID)
	var problem *subagent.Error
	cancelledCode := "unexpected"
	if errors.As(cancelledErr, &problem) {
		cancelledCode = string(problem.Code)
	}
	goObservation := map[string]any{
		"entries":   catalogContractEntries(entries),
		"cancelled": cancelledCode,
	}
	goOutput := catalogContractJSON(t, goObservation)
	sourceOutput := sourceCatalogJSON(
		t,
		catalogSourceOutput(t, "subagent-catalog-cold.ts"),
	)
	if !reflect.DeepEqual(goOutput, sourceOutput) {
		t.Fatalf("Go observation = %#v, source observation = %#v", goOutput, sourceOutput)
	}
}
