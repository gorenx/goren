package configactor

import (
	"context"
	"errors"
	"github.com/gorenx/goren/llm/modelconfig"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// ErrUnavailable means composition has not initialized this owner.
var ErrUnavailable = errors.New("modelconfig: owner is unavailable")

// Grain serializes configuration operations using the framework mailbox.
type Grain struct {
	catalog *modelconfig.Catalog
}

// OnActivate waits for explicit composition initialization.
func (*Grain) OnActivate(context.Context, *goakt.GrainProps) error { return nil }

// OnDeactivate releases no external resources.
func (*Grain) OnDeactivate(context.Context, *goakt.GrainProps) error { return nil }

// OnReceive maps named requests to pure business operations.
func (owner *Grain) OnReceive(gc *goakt.GrainContext) {
	switch incoming := gc.Message().(type) {
	case Initialize:
		if owner.catalog != nil || incoming.Catalog == nil {
			gc.Response(Completion{
				Err: ErrUnavailable,
			})
			return
		}
		owner.catalog = incoming.Catalog
		gc.Response(Completion{})
	case Read:
		if owner.catalog == nil {
			gc.Response(Configuration{
				Err: ErrUnavailable,
			})
			return
		}
		snapshot, err := owner.catalog.Read(incoming.Version)
		gc.Response(Configuration{
			Snapshot: snapshot,
			Err:      err,
		})
	case Resolve:
		if owner.catalog == nil {
			gc.Response(Resolution{
				Err: ErrUnavailable,
			})
			return
		}
		resolved, err := owner.catalog.Resolve(incoming.Version, incoming.Overrides)
		gc.Response(Resolution{
			Resolved: resolved,
			Err:      err,
		})
	case Replace:
		if owner.catalog == nil {
			gc.Response(Configuration{
				Err: ErrUnavailable,
			})
			return
		}
		snapshot, err := owner.catalog.Replace(incoming.ExpectedVersion, incoming.Definition)
		gc.Response(Configuration{
			Snapshot: snapshot,
			Err:      err,
		})
	default:
		gc.Unhandled()
	}
}
