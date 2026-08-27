package bound

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

// appliedProvisioner appends the exact parent config reference while the
// child Agent is still unpublished. It owns no prompt or Tool composition.
type appliedProvisioner struct {
	parentID       session.SessionID
	configEventSeq int64
	revision       int64
}

func (source *appliedProvisioner) Provision(
	ctx context.Context,
	target agent.Scope,
) (agent.Provisioning, error) {
	if source == nil || target == nil || target.Agent() == nil ||
		target.Agent().SessionValue() == nil {
		return nil, errors.New(
			"subagent: Bound applied config target is unavailable",
		)
	}
	draft, err := session.NewEventDraft(
		subagent.BoundConfigAppliedEvent,
		subagent.BoundConfigAppliedData{
			Version:              subagent.BoundEventVersion,
			ParentSessionID:      source.parentID,
			ParentConfigEventSeq: source.configEventSeq,
			Revision:             source.revision,
		},
	)
	if err != nil {
		return nil, err
	}
	return &appliedProvisioning{
		ctx:          ctx,
		conversation: target.Agent().SessionValue(),
		draft:        draft,
	}, nil
}

type appliedProvisioning struct {
	ctx          context.Context
	conversation session.Context
	draft        session.EventDraft
}

func (acquired *appliedProvisioning) Commit() error {
	if acquired == nil || acquired.conversation == nil {
		return errors.New("subagent: Bound applied config is unavailable")
	}
	_, err := acquired.conversation.Commit(
		acquired.ctx,
		session.Batch(acquired.draft),
	)
	return err
}

func (*appliedProvisioning) Dispose(context.Context) error {
	return nil
}

var _ agent.Provisioner = (*appliedProvisioner)(nil)
var _ agent.Provisioning = (*appliedProvisioning)(nil)
