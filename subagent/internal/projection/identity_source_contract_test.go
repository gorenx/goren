//go:build contract

package projection

import (
	"encoding/json"
	"testing"

	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

func TestPinnedSourceIdentityProjectionMatchesGo(t *testing.T) {
	assertProjectionContract(
		t,
		"subagent-identity-projection.ts",
		[]projectionContractObservation{
			observeProjection(t, "last-wins", identityUnit{}, []session.Event{
				projectionContractEvent(subagent.DescriptorEventName, 2, 100, json.RawMessage(
					`{"version":2,"mode":"one-shot","provider":"spawn","label":"ancestor"}`,
				)),
				projectionContractEvent(subagent.DescriptorEventName, 8, 200, json.RawMessage(
					`{"version":2,"mode":"continuable","provider":"fork","label":"child"}`,
				)),
			}),
			observeProjection(t, "damage-resets", identityUnit{}, []session.Event{
				projectionContractEvent(subagent.DescriptorEventName, 2, 100, json.RawMessage(
					`{"version":2,"mode":"one-shot","provider":"spawn"}`,
				)),
				projectionContractEvent(subagent.DescriptorEventName, 9, 300, json.RawMessage(
					`{"version":2,"mode":"continuable"}`,
				)),
			}),
			observeProjection(t, "unsupported-resets", identityUnit{}, []session.Event{
				projectionContractEvent(subagent.DescriptorEventName, 2, 100, json.RawMessage(
					`{"version":2,"mode":"one-shot","provider":"spawn"}`,
				)),
				projectionContractEvent(subagent.DescriptorEventName, 9, 300, json.RawMessage(
					`{"version":3}`,
				)),
			}),
		},
	)
}
