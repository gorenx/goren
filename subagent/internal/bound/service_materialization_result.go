package bound

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

func (owner *Service) finishFailedMaterialization(
	ctx context.Context,
	parentAgent agent.Agent,
	childID session.SessionID,
	revision int64,
	materializationErr error,
) error {
	owner.reportMaterializationFailure(
		parentAgent.ID(),
		childID,
		materializationErr,
	)
	stateErr := owner.recordMaterialization(
		context.WithoutCancel(ctx),
		parentAgent,
		childID,
		revision,
		subagent.BoundMaterializationFailed,
	)
	return errors.Join(materializationErr, stateErr)
}

func (owner *Service) disposeFailedMaterialization(
	ctx context.Context,
	parentAgent agent.Agent,
	childID session.SessionID,
	revision int64,
	handle agent.Handle,
	materializationErr error,
) error {
	disposeErr := handle.Dispose(context.WithoutCancel(ctx))
	return errors.Join(
		owner.finishFailedMaterialization(
			ctx,
			parentAgent,
			childID,
			revision,
			materializationErr,
		),
		disposeErr,
	)
}

func (owner *Service) recordMaterialization(
	ctx context.Context,
	parentAgent agent.Agent,
	childID session.SessionID,
	revision int64,
	result subagent.BoundMaterializationResult,
) error {
	draft, err := session.NewEventDraft(
		subagent.BoundMaterializationEvent,
		subagent.BoundMaterializationData{
			Version:        subagent.BoundEventVersion,
			ChildSessionID: childID,
			ConfigRevision: revision,
			Result:         result,
		},
	)
	if err != nil {
		return err
	}
	if _, err = parentAgent.SessionValue().Commit(
		ctx,
		session.Batch(draft),
	); err != nil {
		return err
	}
	return owner.dependencies.Sessions.Flush(
		ctx,
		parentAgent.SessionValue(),
	)
}

func (owner *Service) reportMaterializationFailure(
	parentID session.SessionID,
	childID session.SessionID,
	materializationErr error,
) {
	if owner.dependencies.Failures == nil {
		return
	}
	owner.dependencies.Failures.ReportBoundMaterializationFailure(
		MaterializationFailure{
			ParentID: parentID,
			ChildID:  childID,
			Error:    materializationErr,
		},
	)
}
