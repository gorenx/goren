package bound

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
	sesspersist "github.com/gorenx/goren/session/persistence"
	"github.com/gorenx/goren/subagent"
	sharedexecution "github.com/gorenx/goren/subagent/internal/execution"
	"github.com/gorenx/goren/subagent/internal/lineage"
	subagentprojection "github.com/gorenx/goren/subagent/internal/projection"
)

// Bind commits the immutable creation input and config revision 1 atomically
// in the exact parent Session. It does not create the child Agent.
func (owner *Service) Bind(
	ctx context.Context,
	command subagent.BindCommand,
) (subagent.BoundBinding, error) {
	if err := checkContext(ctx, "Bound Bind"); err != nil {
		return subagent.BoundBinding{}, err
	}
	if err := owner.authorizeParent(command.Parent); err != nil {
		return subagent.BoundBinding{}, err
	}
	if owner.dependencies.Projections == nil {
		return subagent.BoundBinding{}, unavailableDependency("projections")
	}
	if owner.dependencies.SeedBuilders == nil {
		return subagent.BoundBinding{}, unavailableDependency("SeedBuilders")
	}
	childLineage, err := lineage.From(command.Parent, command.MaxDepth)
	if err != nil {
		return subagent.BoundBinding{}, err
	}
	resolvedOptions := childLineage.AgentOptions(command.AgentOptions)
	creation := subagent.BoundCreation{
		SeedBuilder:   command.SeedBuilder,
		Title:         command.Title,
		InitialPrompt: command.InitialPrompt,
		AgentOptions:  resolvedOptions,
	}
	if _, err = json.Marshal(creation); err != nil {
		return subagent.BoundBinding{}, err
	}
	if _, found := owner.dependencies.SeedBuilders.Find(creation.SeedBuilder); !found {
		return subagent.BoundBinding{}, noSeedBuilder(creation.SeedBuilder)
	}
	config, err := subagent.SnapshotBoundConfig(command.Config)
	if err != nil {
		return subagent.BoundBinding{}, err
	}
	if err = owner.dependencies.Extensions.Validate(config.Extensions); err != nil {
		return subagent.BoundBinding{}, err
	}
	childID, err := requestedChildID(command.RequestedChildID)
	if err != nil {
		return subagent.BoundBinding{}, err
	}
	parentSession := command.Parent.SessionValue()
	parentLock := owner.parentOperation(command.Parent.ID())
	parentLock.mutex.Lock()
	defer parentLock.mutex.Unlock()
	currentOperation := owner.childOperation(command.Parent.ID(), childID)
	currentOperation.mutex.Lock()
	defer currentOperation.mutex.Unlock()
	if err = checkContext(ctx, "Bound Bind"); err != nil {
		return subagent.BoundBinding{}, err
	}
	if err = owner.authorizeParent(command.Parent); err != nil {
		return subagent.BoundBinding{}, err
	}
	if err = owner.assertChildAvailable(ctx, childID); err != nil {
		return subagent.BoundBinding{}, err
	}
	view, err := owner.parentView(parentSession)
	if err != nil {
		return subagent.BoundBinding{}, err
	}
	if err = validateBindingAvailability(view, childID, creation.Title); err != nil {
		return subagent.BoundBinding{}, err
	}
	bindingDraft, err := session.NewEventDraft(
		subagent.BoundBindingEvent,
		subagent.BoundBindingData{
			Version:        subagent.BoundEventVersion,
			ChildSessionID: childID,
			Creation:       creation,
		},
	)
	if err != nil {
		return subagent.BoundBinding{}, err
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
		return subagent.BoundBinding{}, err
	}
	if _, err = parentSession.Commit(
		ctx,
		session.Batch(bindingDraft, configDraft),
	); err != nil {
		return subagent.BoundBinding{}, err
	}
	if owner.dependencies.Sessions == nil {
		return subagent.BoundBinding{}, unavailableDependency("Session LiveStore")
	}
	if err = owner.dependencies.Sessions.Flush(ctx, parentSession); err != nil {
		return subagent.BoundBinding{}, err
	}
	return subagent.BoundBinding{
		ParentSessionID: command.Parent.ID(),
		ChildSessionID:  childID,
		ConfigRevision:  1,
	}, nil
}

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

// HasBinding reports only an exact parent-owned durable binding. It does not
// inspect or materialize the child.
func (owner *Service) HasBinding(
	ctx context.Context,
	parentAgent agent.Agent,
	childID session.SessionID,
) (bool, error) {
	if err := checkContext(ctx, "Bound HasBinding"); err != nil {
		return false, err
	}
	if err := owner.authorizeParent(parentAgent); err != nil {
		return false, err
	}
	view, err := owner.parentView(parentAgent.SessionValue())
	if err != nil {
		return false, err
	}
	_, found := view.Binding(childID)
	return found, nil
}

func (owner *Service) parentView(
	parentSession session.Context,
) (subagentprojection.Bound, error) {
	if owner.dependencies.Projections == nil {
		return subagentprojection.Bound{}, unavailableDependency("projections")
	}
	snapshot, err := owner.dependencies.Projections.Snapshot(parentSession)
	if err != nil {
		return subagentprojection.Bound{}, err
	}
	view, found, err := subagentprojection.ReadBound(snapshot.Values)
	if err != nil {
		return subagentprojection.Bound{}, err
	}
	if !found {
		return subagentprojection.Bound{}, errors.New(
			"subagent: Bound projection is not registered",
		)
	}
	return view, nil
}

func (owner *Service) assertChildAvailable(
	ctx context.Context,
	childID session.SessionID,
) error {
	if owner.dependencies.Agents == nil || owner.dependencies.Sessions == nil ||
		owner.dependencies.Persistence == nil {
		return unavailableDependency("child identity lookup")
	}
	if _, found := owner.dependencies.Agents.Get(childID); found {
		return duplicateChild(childID)
	}
	if _, found := owner.dependencies.Sessions.Get(childID); found {
		return duplicateChild(childID)
	}
	_, err := owner.dependencies.Persistence.Inspect(ctx, childID)
	if err == nil {
		return duplicateChild(childID)
	}
	var notFound *sesspersist.NotFoundError
	if errors.As(err, &notFound) {
		return nil
	}
	return err
}

func validateBindingAvailability(
	view subagentprojection.Bound,
	childID session.SessionID,
	title string,
) error {
	for _, binding := range view.Bindings {
		if binding.ChildSessionID == childID {
			return duplicateChild(childID)
		}
		if binding.Creation.Title == title {
			return &subagent.Error{
				Code: subagent.ErrorDuplicateBoundBinding,
				Message: fmt.Sprintf(
					"Bound binding title %q already exists in parent Session",
					title,
				),
			}
		}
	}
	return nil
}

func requestedChildID(
	requested *session.SessionID,
) (session.SessionID, error) {
	if requested == nil {
		return sharedexecution.NewChildID()
	}
	if !validChildID(*requested) {
		return "", errors.New(
			"subagent: Bound child Session ID must be non-empty and trimmed",
		)
	}
	return *requested, nil
}

func validChildID(childID session.SessionID) bool {
	value := string(childID)
	return strings.TrimSpace(value) != "" && value == strings.TrimSpace(value)
}

func duplicateChild(childID session.SessionID) error {
	return &subagent.Error{
		Code:    subagent.ErrorDuplicateChild,
		Message: fmt.Sprintf("subagent %q already exists", childID),
	}
}

func bindingNotFound(childID session.SessionID) error {
	return &subagent.Error{
		Code: subagent.ErrorBoundBindingNotFound,
		Message: fmt.Sprintf(
			"subagent %q has no Bound binding in this parent Session",
			childID,
		),
	}
}

func configConflict(
	childID session.SessionID,
	expected int64,
	actual int64,
) error {
	return &subagent.Error{
		Code: subagent.ErrorBoundConfigConflict,
		Message: fmt.Sprintf(
			"subagent %q Bound config revision is %d, expected %d",
			childID,
			actual,
			expected,
		),
	}
}

func noSeedBuilder(builderName string) error {
	return &subagent.Error{
		Code: subagent.ErrorNoSeedBuilder,
		Message: fmt.Sprintf(
			"no subagent SeedBuilder registered for %q",
			builderName,
		),
	}
}
