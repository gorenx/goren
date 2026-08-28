package bound

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

func (child *boundChild) finishFailedMaterialization(
	ctx context.Context,
	revision int64,
	materializationErr error,
) error {
	child.reportMaterializationFailure(materializationErr)
	stateErr := child.recordMaterialization(
		context.WithoutCancel(ctx),
		revision,
		subagent.BoundMaterializationFailed,
	)
	return errors.Join(materializationErr, stateErr)
}

func (child *boundChild) disposeFailedMaterialization(
	ctx context.Context,
	revision int64,
	handle agent.Handle,
	materializationErr error,
) error {
	disposeErr := handle.Dispose(context.WithoutCancel(ctx))
	return errors.Join(
		child.finishFailedMaterialization(
			ctx,
			revision,
			materializationErr,
		),
		disposeErr,
	)
}

func (child *boundChild) recordMaterialization(
	ctx context.Context,
	revision int64,
	result subagent.BoundMaterializationResult,
) error {
	draft, err := session.NewEventDraft(
		subagent.BoundMaterializationEvent,
		subagent.BoundMaterializationData{
			Version:        subagent.BoundEventVersion,
			ChildSessionID: child.key.childID,
			ConfigRevision: revision,
			Result:         result,
		},
	)
	if err != nil {
		return err
	}
	if _, err = child.parent.SessionValue().Commit(
		ctx,
		session.Batch(draft),
	); err != nil {
		return err
	}
	return child.sessions.Flush(
		ctx,
		child.parent.SessionValue(),
	)
}

func (child *boundChild) reportMaterializationFailure(
	materializationErr error,
) {
	if child.failures == nil {
		return
	}
	child.failures.ReportBoundMaterializationFailure(
		MaterializationFailure{
			ParentID: child.key.parentID,
			ChildID:  child.key.childID,
			Error:    materializationErr,
		},
	)
}
