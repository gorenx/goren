package credentialactor

import (
	"context"
	"errors"

	"github.com/gorenx/goren/credentials"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// ErrUnavailable means the Credentials Actor has not been initialized.
var ErrUnavailable = errors.New("credentials: owner is unavailable")

// Grain adapts sequential get/set messages to the framework-free Manager.
// Mailbox ordering and lifecycle belong to GoAkt, not a second state machine.
type Grain struct {
	manager *credentials.Manager
}

// OnActivate acquires no resources before composition supplies the Manager.
func (*Grain) OnActivate(context.Context, *goakt.GrainProps) error {
	return nil
}

// OnDeactivate needs no cleanup: each Manager operation closes its own I/O.
func (*Grain) OnDeactivate(context.Context, *goakt.GrainProps) error {
	return nil
}

// OnReceive finishes one operation before the framework dispatches the next.
func (owner *Grain) OnReceive(gc *goakt.GrainContext) {
	switch incoming := gc.Message().(type) {
	case Initialize:
		if owner.manager != nil || incoming.Manager == nil {
			gc.Response(Completion{
				Err: ErrUnavailable,
			})
			return
		}
		owner.manager = incoming.Manager
		gc.Response(Completion{})
	case Resolve:
		if err := owner.validate(incoming.Context); err != nil {
			gc.Response(Resolution{
				Err: err,
			})
			return
		}
		resolved, err := owner.manager.Resolve(incoming.Context, incoming.Ref)
		gc.Response(Resolution{
			Resolved: resolved,
			Err:      err,
		})
	case Describe:
		if err := owner.validate(incoming.Context); err != nil {
			gc.Response(Description{
				Err: err,
			})
			return
		}
		info, err := owner.manager.Describe(incoming.Context, incoming.Ref)
		gc.Response(Description{
			Info: info,
			Err:  err,
		})
	case Set:
		if err := owner.validate(incoming.Context); err != nil {
			gc.Response(Completion{
				Err: err,
			})
			return
		}
		gc.Response(Completion{
			Err: owner.manager.Set(incoming.Context, incoming.Ref, incoming.Value),
		})
	case Unset:
		if err := owner.validate(incoming.Context); err != nil {
			gc.Response(Completion{
				Err: err,
			})
			return
		}
		gc.Response(Completion{
			Err: owner.manager.Unset(incoming.Context, incoming.Ref),
		})
	default:
		gc.Unhandled()
	}
}

func (owner *Grain) validate(ctx context.Context) error {
	if owner.manager == nil {
		return ErrUnavailable
	}
	if ctx == nil {
		return errors.New("credentials: request context is nil")
	}
	return nil
}
