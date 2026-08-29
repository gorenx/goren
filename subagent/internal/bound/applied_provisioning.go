package bound

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
)

type appliedProvisioning struct {
	ctx          context.Context
	conversation session.Context
	draft        session.EventDraft
}

func (acquired *appliedProvisioning) Commit() error {
	if acquired == nil || acquired.conversation == nil {
		return errors.New("subagent: applied Bound Definition is unavailable")
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

var _ agent.Provisioning = (*appliedProvisioning)(nil)
