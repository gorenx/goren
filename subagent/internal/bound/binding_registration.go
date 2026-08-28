package bound

import (
	"context"

	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	subagentprojection "github.com/gorenx/goren/subagent/internal/projection"
)

// bindingRegistration is the atomic parent-Session change that introduces
// one immutable binding and its initial config revision.
type bindingRegistration struct {
	childID      session.SessionID
	title        string
	bindingDraft session.EventDraft
	configDraft  session.EventDraft
}

func newBindingRegistration(
	childID session.SessionID,
	creation subagent.BoundCreation,
	config subagent.BoundConfigSnapshot,
) (bindingRegistration, error) {
	bindingDraft, err := session.NewEventDraft(
		subagent.BoundBindingEvent,
		subagent.BoundBindingData{
			Version:        subagent.BoundEventVersion,
			ChildSessionID: childID,
			Creation:       creation,
		},
	)
	if err != nil {
		return bindingRegistration{}, err
	}
	configDraft, err := session.NewEventDraft(
		subagent.BoundConfigEvent,
		subagent.BoundConfigData{
			Version:          subagent.BoundEventVersion,
			ChildSessionID:   childID,
			PreviousRevision: 0,
			Revision:         1,
			Config:           config,
		},
	)
	if err != nil {
		return bindingRegistration{}, err
	}
	return bindingRegistration{
		childID:      childID,
		title:        creation.Title,
		bindingDraft: bindingDraft,
		configDraft:  configDraft,
	}, nil
}

func (registration bindingRegistration) Build(
	_ context.Context,
	snapshot session.Snapshot,
) ([]session.EventDraft, error) {
	view, err := subagentprojection.FoldBound(snapshot.Events)
	if err != nil {
		return nil, err
	}
	if err = validateBindingAvailability(
		view,
		registration.childID,
		registration.title,
	); err != nil {
		return nil, err
	}
	return []session.EventDraft{
		registration.bindingDraft,
		registration.configDraft,
	}, nil
}

var _ session.WritePlan = bindingRegistration{}
