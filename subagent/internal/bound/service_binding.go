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
