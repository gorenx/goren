package cacheactor

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/projectioncache"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/passivation"
	"github.com/tochemey/goakt/v4/reentrancy"
)

// Grain owns confirmed records for the SessionID used as its framework name.
// The framework stashes ordinary messages while a Writer request is outstanding.
type Grain struct {
	sessionID session.SessionID
	records   projectioncache.Cache
	worker    *goakt.PID
}

// OnActivate waits for explicit storage initialization.
func (*Grain) OnActivate(context.Context, *goakt.GrainProps) error { return nil }

// OnDeactivate releases the private Writer, never the shared storage adapter.
func (owner *Grain) OnDeactivate(ctx context.Context, props *goakt.GrainProps) error {
	return owner.stopWriter(ctx, props.ActorSystem())
}

// OnReceive validates business inputs and delegates storage through async requests.
func (owner *Grain) OnReceive(gc *goakt.GrainContext) {
	switch input := gc.Message().(type) {
	case Initialize:
		owner.initialize(gc, input)
	case Write:
		owner.write(gc, input)
	case ColdSnapshot:
		owner.coldSnapshot(gc, input)
	case CachedSnapshot:
		owner.cachedSnapshot(gc, input)
	case Read:
		if owner.worker == nil {
			gc.Response(Record{
				Err: errors.New("projection cache: storage is not initialized"),
			})
			return
		}
		if input.Header.ID != owner.sessionID {
			gc.Response(Record{
				Err: errors.New("projection cache: read belongs to another Session"),
			})
			return
		}
		record, found := owner.records.Match(input.Header)
		if !found {
			gc.Response(Record{})
			return
		}
		gc.Response(Record{
			Value: &record,
		})
	case Close:
		gc.Response(Completion{
			Err: owner.stopWriter(gc.Context(), gc.ActorSystem()),
		})
	default:
		gc.Unhandled()
	}
}

func (owner *Grain) initialize(gc *goakt.GrainContext, input Initialize) {
	if input.SessionID == "" || gc.Self().Name() != RuntimeName(string(input.SessionID)) || owner.worker != nil || input.Storage == nil {
		gc.Response(Completion{
			Err: errors.New("projection cache: storage is absent or already initialized"),
		})
		return
	}
	if err := gc.EnableReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.StashNonReentrant))); err != nil {
		gc.Response(Completion{
			Err: err,
		})
		return
	}
	var records projectioncache.Cache
	if input.Record != nil {
		if err := records.Confirm(input.SessionID, *input.Record); err != nil {
			gc.Response(Completion{
				Err: err,
			})
			return
		}
	}
	worker, err := gc.ActorSystem().Spawn(
		gc.Context(),
		"checkpoint-writer-"+uuid.NewString(),
		&writer{
			identifier: input.SessionID,
			backend:    input.Storage,
		},
		// Cache retains this writer until Close; idle time must not revoke it.
		goakt.WithPassivationStrategy(passivation.NewLongLivedStrategy()),
	)
	if err == nil {
		owner.sessionID = input.SessionID
		owner.worker = worker
		owner.records = records
	}
	gc.Response(Completion{
		Err: err,
	})
}

func (owner *Grain) write(gc *goakt.GrainContext, input Write) {
	if owner.worker == nil || input.Header.ID != owner.sessionID {
		gc.Response(Completion{
			Err: errors.New("projection cache: storage is unavailable or Session does not match"),
		})
		return
	}
	record, err := projectioncache.NewCheckpoint(input.Header, input.Rows, input.Durable)
	if err != nil {
		gc.Response(Completion{
			Err: err,
		})
		return
	}
	prepared, err := owner.records.Prepare(input.Header.ID, record)
	if err != nil || prepared == nil {
		gc.Response(Completion{
			Err: err,
		})
		return
	}
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(Completion{
			Err: errors.New("projection cache: write requires an asynchronous request"),
		})
		return
	}
	identifier := input.Header.ID
	gc.RequestActor(owner.worker.Name(), write{
		record: *prepared,
	}, goakt.WithRequestTimeout(0)).Then(func(payload any, failure error) {
		if failure == nil {
			result, ok := payload.(Completion)
			if !ok {
				failure = errors.New("projection cache: invalid storage acknowledgement")
			} else {
				failure = result.Err
			}
		}
		if failure == nil {
			failure = owner.records.Confirm(identifier, *prepared)
		}
		reply.Response(Completion{
			Err: failure,
		})
	})
}

func (owner *Grain) stopWriter(ctx context.Context, system goakt.ActorSystem) error {
	if owner.worker == nil {
		return nil
	}
	if err := system.Kill(ctx, owner.worker.Name()); err != nil {
		return err
	}
	owner.worker = nil
	return nil
}
