//go:build contract

package projection

import (
	"encoding/json"
	"testing"

	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

func TestPinnedSourceTimingProjectionMatchesGo(t *testing.T) {
	assertProjectionContract(
		t,
		"subagent-timing-projection.ts",
		[]projectionContractObservation{
			observeProjection(t, "reset-and-active", timingUnit{}, []session.Event{
				projectionContractEvent(session.TurnStartEventName, 0, 100, json.RawMessage(`{}`)),
				projectionContractEvent(subagent.DescriptorEventName, 1, 110, json.RawMessage(`{}`)),
				projectionContractEvent(session.TurnEndEventName, 2, 300, json.RawMessage(`{}`)),
				projectionContractEvent(session.TurnStartEventName, 3, 1_000, json.RawMessage(`{}`)),
				projectionContractEvent(subagent.DescriptorEventName, 4, 1_100, json.RawMessage(`{}`)),
				projectionContractEvent(session.TurnEndEventName, 5, 4_100, json.RawMessage(`{}`)),
				projectionContractEvent(session.TurnStartEventName, 6, 10_000, json.RawMessage(`{}`)),
				projectionContractEvent(session.AssistantChunkEventName, 7, 10_500, json.RawMessage(`{}`)),
			}),
			observeProjection(t, "closed-seed", timingUnit{}, []session.Event{
				projectionContractEvent(session.TurnStartEventName, 0, 100, json.RawMessage(`{}`)),
				projectionContractEvent(session.TurnEndEventName, 1, 200, json.RawMessage(`{}`)),
				projectionContractEvent(subagent.DescriptorEventName, 2, 300, json.RawMessage(`{}`)),
			}),
			observeProjection(t, "clamps-negative-duration", timingUnit{}, []session.Event{
				projectionContractEvent(subagent.DescriptorEventName, 0, 100, json.RawMessage(`{}`)),
				projectionContractEvent(session.TurnStartEventName, 1, 500, json.RawMessage(`{}`)),
				projectionContractEvent(session.TurnEndEventName, 2, 400, json.RawMessage(`{}`)),
			}),
		},
	)
}
