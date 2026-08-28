package bound

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

func (child *boundChild) handleMessage(
	ctx context.Context,
	messageValue agentmessage.UserMessage,
) (agentmessage.MessageID, error) {
	if _, err := child.handleStart(ctx); err != nil {
		return "", err
	}
	current := child.current
	if current == nil {
		return "", errors.New(
			"subagent: Bound resident Agent is unavailable",
		)
	}
	if err := current.handle.Subject.Followup(
		messageValue,
	); err != nil {
		return "", err
	}
	return messageValue.StableID(), nil
}

func (child *boundChild) handleConfig(
	requestContext context.Context,
	expectedRevision int64,
	config subagent.BoundConfigSnapshot,
) (subagent.UpdateBoundConfigResult, error) {
	workContext, cancelWork := child.operationContext(requestContext)
	defer cancelWork()
	if err := checkContext(workContext, "Bound UpdateConfig"); err != nil {
		return subagent.UpdateBoundConfigResult{}, err
	}
	if err := child.authorizeParent(); err != nil {
		return subagent.UpdateBoundConfigResult{}, err
	}
	parentSession := child.parent.SessionValue()
	view, err := readBoundProjection(child.projections, parentSession)
	if err != nil {
		return subagent.UpdateBoundConfigResult{}, err
	}
	if _, found := view.Binding(child.key.childID); !found {
		return subagent.UpdateBoundConfigResult{}, bindingNotFound(
			child.key.childID,
		)
	}
	currentConfig, found := view.Config(child.key.childID)
	if !found {
		return subagent.UpdateBoundConfigResult{}, errors.New(
			"subagent: Bound binding has no config",
		)
	}
	if currentConfig.Revision != expectedRevision {
		return subagent.UpdateBoundConfigResult{}, configConflict(
			child.key.childID,
			expectedRevision,
			currentConfig.Revision,
		)
	}
	nextRevision := currentConfig.Revision + 1
	draft, err := session.NewEventDraft(
		subagent.BoundConfigEvent,
		subagent.BoundConfigData{
			Version:          subagent.BoundEventVersion,
			ChildSessionID:   child.key.childID,
			PreviousRevision: currentConfig.Revision,
			Revision:         nextRevision,
			Config:           config,
		},
	)
	if err != nil {
		return subagent.UpdateBoundConfigResult{}, err
	}
	if _, err = parentSession.Commit(
		workContext,
		session.Batch(draft),
	); err != nil {
		return subagent.UpdateBoundConfigResult{}, err
	}
	result := subagent.UpdateBoundConfigResult{
		ParentSessionID: child.key.parentID,
		ChildSessionID:  child.key.childID,
		Revision:        nextRevision,
	}
	if child.sessions == nil {
		return result, unavailableDependency("Session LiveStore")
	}
	if err = child.sessions.Flush(workContext, parentSession); err != nil {
		return result, err
	}
	if child.current == nil {
		return result, nil
	}
	_, err = child.handleStart(workContext)
	var typed *subagent.Error
	if errors.As(err, &typed) && typed.Code == subagent.ErrorBoundDisabled {
		return result, nil
	}
	return result, err
}
