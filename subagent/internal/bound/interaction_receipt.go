package bound

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

func countReceipts(
	childSession session.Context,
	want subagent.Delivery,
) (int, error) {
	if childSession == nil {
		return 0, errors.New("subagent: Bound child Session is unavailable")
	}
	canonical, err := want.CloneSource()
	if err != nil {
		return 0, err
	}
	want = canonical.(subagent.Delivery)
	startSeq := int64(0)
	if seedLength := childSession.Header().SeedLength; seedLength != nil {
		startSeq = *seedLength
	}
	count := 0
	for _, committed := range childSession.Events() {
		if committed.Seq < startSeq || committed.Type != agent.InboxSplicedEventName {
			continue
		}
		var splice agent.InboxSplice
		if err := json.Unmarshal(committed.Data, &splice); err != nil {
			return 0, fmt.Errorf(
				"subagent: decode child Inbox receipt at seq %d: %w",
				committed.Seq,
				err,
			)
		}
		for _, messageValue := range splice.Inserted {
			origin := messageValue.SourceValue()
			if origin == nil || origin.SourceKind() != subagent.DeliveryKind {
				continue
			}
			delivery, err := subagent.DecodeDelivery(origin)
			if err != nil {
				return 0, fmt.Errorf(
					"subagent: decode child interaction receipt at seq %d: %w",
					committed.Seq,
					err,
				)
			}
			if delivery == want {
				count++
			}
		}
	}
	if count > 1 {
		return 0, errors.New(
			"subagent: child Session contains duplicate parent interaction receipts",
		)
	}
	return count, nil
}
