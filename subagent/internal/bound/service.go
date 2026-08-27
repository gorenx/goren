// Package bound owns the Bound Subagent mode whose initial start is selected
// from a durable parent binding.
package bound

import (
	"context"
	"errors"

	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

// ErrStartNotImplemented marks the intentionally incomplete Bound start use
// case. Callers must contain this failure so parent publication can continue.
var ErrStartNotImplemented = errors.New(
	"subagent: Bound Start is not implemented",
)

// Service owns Bound mode behavior. Configuration-source dependencies will be
// added with the binding projection before Start is implemented.
type Service struct{}

// New constructs the Bound mode service.
func New() *Service {
	return &Service{}
}

// Mode identifies the business mode implemented by Service.
func (*Service) Mode() subagent.Mode {
	return subagent.ModeBound
}

// Start will load Bound-owned creation input and current configuration before
// creating or restoring the child identified by the committed binding.
func (*Service) Start(
	ctx context.Context,
	command subagent.BoundStartCommand,
) (subagent.Execution, error) {
	if ctx == nil {
		return nil, errors.New("subagent: Bound Start context is nil")
	}
	if requestErr := ctx.Err(); requestErr != nil {
		return nil, requestErr
	}
	if command.Parent() == nil || command.ChildID() == "" {
		return nil, errors.New("subagent: Bound Start command is incomplete")
	}
	// TODO(bound-start): load the committed binding's creation input and latest
	// configuration, then create or restore one Bound Execution.
	return nil, ErrStartNotImplemented
}

// Interrupt is currently a no-op because Start cannot publish a Bound
// Execution. It becomes execution-aware together with Start.
func (*Service) Interrupt(
	ctx context.Context,
	_ session.SessionID,
) error {
	if ctx == nil {
		return errors.New("subagent: Bound Interrupt context is nil")
	}
	return ctx.Err()
}

// Close has no live Bound resources until Start is implemented.
func (*Service) Close(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
