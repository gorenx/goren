package promptactor

import (
	"context"
	"errors"
	"github.com/gorenx/goren/systemprompt"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// ErrUnavailable means the prompt registration owner has not been initialized.
var ErrUnavailable = errors.New("systemprompt: owner is unavailable")

// Grain serializes definition changes and captures snapshots, never evaluating providers.
type Grain struct {
	layer   *systemprompt.PromptLayer
	version uint64
}

// OnActivate waits for explicit composition initialization.
func (*Grain) OnActivate(context.Context, *goakt.GrainProps) error { return nil }

// OnDeactivate releases registration state after framework message processing ends.
func (owner *Grain) OnDeactivate(ctx context.Context, _ *goakt.GrainProps) error {
	if owner.layer == nil {
		return nil
	}
	return owner.layer.Close(ctx)
}

// OnReceive handles only named registration and snapshot messages.
func (owner *Grain) OnReceive(gc *goakt.GrainContext) {
	switch incoming := gc.Message().(type) {
	case Initialize:
		if owner.layer != nil || incoming.Layer == nil {
			gc.Response(Completion{
				Err: ErrUnavailable,
			})
			return
		}
		owner.layer = incoming.Layer
		owner.version = 1
		gc.Response(Completion{})
	case ApplyBaseline:
		if owner.layer == nil {
			gc.Response(Completion{
				Err: ErrUnavailable,
			})
			return
		}
		err := owner.layer.ApplyBaseline(incoming.Context, incoming.Config)
		if err == nil {
			owner.version++
		}
		gc.Response(Completion{
			Err: err,
		})
	case Prepare:
		if owner.layer == nil {
			gc.Response(Prepared{
				Err: ErrUnavailable,
			})
			return
		}
		gc.Response(Prepared{
			Version:  owner.version,
			Config:   owner.layer.Configuration(),
			Snapshot: owner.layer.Capture(),
		})
	case Register:
		if owner.layer == nil {
			gc.Response(Registered{
				Err: ErrUnavailable,
			})
			return
		}
		identifier, err := incoming.register(context.Background(), owner.layer)
		if err != nil {
			gc.Response(Registered{
				Err: err,
			})
			return
		}
		owner.version++
		gc.Response(Registered{
			ID:      identifier,
			Version: owner.version,
		})
	case Remove:
		if owner.layer == nil {
			gc.Response(Completion{
				Err: ErrUnavailable,
			})
			return
		}
		removed, err := owner.layer.Remove(context.Background(), incoming.ID)
		if removed {
			owner.version++
		}
		gc.Response(Completion{
			Removed: removed,
			Err:     err,
		})
	default:
		gc.Unhandled()
	}
}
