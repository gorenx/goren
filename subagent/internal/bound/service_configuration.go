package bound

import (
	"context"
	"errors"

	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

// UpdateConfig commits one complete revision and then replaces an already
// resident Bound Agent epoch before later Bound delivery can be admitted.
func (owner *Service) UpdateConfig(
	ctx context.Context,
	command subagent.UpdateBoundConfigCommand,
) (subagent.UpdateBoundConfigResult, error) {
	if err := checkContext(ctx, "Bound UpdateConfig"); err != nil {
		return subagent.UpdateBoundConfigResult{}, err
	}
	if err := owner.authorizeParent(command.Parent); err != nil {
		return subagent.UpdateBoundConfigResult{}, err
	}
	if !validChildID(command.ChildSessionID) {
		return subagent.UpdateBoundConfigResult{}, errors.New(
			"subagent: Bound child Session ID must be non-empty and trimmed",
		)
	}
	if command.ExpectedRevision <= 0 {
		return subagent.UpdateBoundConfigResult{}, errors.New(
			"subagent: Bound expected revision must be positive",
		)
	}
	if owner.dependencies.Projections == nil {
		return subagent.UpdateBoundConfigResult{}, unavailableDependency("projections")
	}
	config, err := subagent.SnapshotBoundConfig(command.Config)
	if err != nil {
		return subagent.UpdateBoundConfigResult{}, err
	}
	if err = owner.dependencies.Extensions.Validate(config.Extensions); err != nil {
		return subagent.UpdateBoundConfigResult{}, err
	}
	currentOperation := owner.childOperation(
		command.Parent.ID(),
		command.ChildSessionID,
	)
	currentOperation.mutex.Lock()
	defer currentOperation.mutex.Unlock()
	if err = checkContext(ctx, "Bound UpdateConfig"); err != nil {
		return subagent.UpdateBoundConfigResult{}, err
	}
	if err = owner.authorizeParent(command.Parent); err != nil {
		return subagent.UpdateBoundConfigResult{}, err
	}
	parentSession := command.Parent.SessionValue()
	view, err := owner.parentView(parentSession)
	if err != nil {
		return subagent.UpdateBoundConfigResult{}, err
	}
	if _, found := view.Binding(command.ChildSessionID); !found {
		return subagent.UpdateBoundConfigResult{}, bindingNotFound(
			command.ChildSessionID,
		)
	}
	current, found := view.Config(command.ChildSessionID)
	if !found {
		return subagent.UpdateBoundConfigResult{}, errors.New(
			"subagent: Bound binding has no config",
		)
	}
	if current.Revision != command.ExpectedRevision {
		return subagent.UpdateBoundConfigResult{}, configConflict(
			command.ChildSessionID,
			command.ExpectedRevision,
			current.Revision,
		)
	}
	nextRevision := current.Revision + 1
	draft, err := session.NewEventDraft(
		subagent.BoundConfigEvent,
		subagent.BoundConfigData{
			Version:          subagent.BoundEventVersion,
			ChildSessionID:   command.ChildSessionID,
			PreviousRevision: current.Revision,
			Revision:         nextRevision,
			Config:           config,
		},
	)
	if err != nil {
		return subagent.UpdateBoundConfigResult{}, err
	}
	if _, err = parentSession.Commit(ctx, session.Batch(draft)); err != nil {
		return subagent.UpdateBoundConfigResult{}, err
	}
	result := subagent.UpdateBoundConfigResult{
		ParentSessionID: command.Parent.ID(),
		ChildSessionID:  command.ChildSessionID,
		Revision:        nextRevision,
	}
	if owner.dependencies.Sessions == nil {
		return result, unavailableDependency("Session LiveStore")
	}
	if err = owner.dependencies.Sessions.Flush(ctx, parentSession); err != nil {
		return result, err
	}
	if err = owner.replaceResidentLocked(
		ctx,
		command.Parent,
		command.ChildSessionID,
	); err != nil {
		return result, err
	}
	return result, nil
}
