package bound

import (
	"context"
	"errors"

	"github.com/gorenx/goren/session"
	boundcontract "github.com/gorenx/goren/subagent/bound"
	subagentprojection "github.com/gorenx/goren/subagent/internal/projection"
)

type pendingBinding struct {
	name    string
	childID session.SessionID
}

// bindingRegistration atomically introduces every still-missing enabled
// Definition Binding at one parent Session FIFO head.
type bindingRegistration struct {
	bindings []pendingBinding
}

func (registration bindingRegistration) Build(
	_ context.Context,
	snapshot session.Snapshot,
) ([]session.EventDraft, error) {
	view, err := subagentprojection.FoldBound(snapshot.Events)
	if err != nil {
		return nil, err
	}
	contextNextSeq := completedContextNextSeq(snapshot.Events)
	drafts := make([]session.EventDraft, 0, len(registration.bindings))
	for _, pending := range registration.bindings {
		if _, found := view.BindingNamed(pending.name); found {
			continue
		}
		if _, found := view.Binding(pending.childID); found {
			return nil, errors.New(
				"subagent: duplicate Bound child Session ID",
			)
		}
		draft, draftErr := session.NewEventDraft(
			boundcontract.BindingEvent,
			boundcontract.BindingData{
				Version:        boundcontract.EventVersion,
				Name:           pending.name,
				ChildSessionID: pending.childID,
				ContextNextSeq: contextNextSeq,
			},
		)
		if draftErr != nil {
			return nil, draftErr
		}
		drafts = append(drafts, draft)
	}
	return drafts, nil
}

func completedContextNextSeq(events []session.Event) int64 {
	for index := len(events) - 1; index >= 0; index-- {
		if events[index].Type == session.TurnEndEventName {
			return events[index].Seq + 1
		}
	}
	return 0
}

var _ session.WritePlan = bindingRegistration{}
