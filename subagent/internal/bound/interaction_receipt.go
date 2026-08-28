package bound

import (
	"errors"
	"fmt"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

func hasReceipt(
	childSession session.Context,
	want subagent.Delivery,
) (bool, error) {
	if childSession == nil {
		return false, errors.New(
			"subagent: Bound child Session is unavailable",
		)
	}
	canonical, err := want.CloneSource()
	if err != nil {
		return false, err
	}
	want = canonical.(subagent.Delivery)
	startSeq := int64(0)
	if seedLength := childSession.Header().SeedLength; seedLength != nil {
		startSeq = *seedLength
	}
	found := false
	for _, committed := range childSession.Events() {
		if committed.Seq < startSeq || committed.Type != agent.InboxSplicedEventName {
			continue
		}
		var splice agent.InboxSplice
		if err := decodeInteractionJSON(committed.Data, &splice); err != nil {
			return false, fmt.Errorf(
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
				return false, fmt.Errorf(
					"subagent: decode child interaction receipt at seq %d: %w",
					committed.Seq,
					err,
				)
			}
			if delivery == want {
				if found {
					return false, errors.New(
						"subagent: child Session contains duplicate parent interaction receipts",
					)
				}
				found = true
			}
		}
	}
	return found, nil
}
