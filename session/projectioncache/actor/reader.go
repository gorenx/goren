package cacheactor

import (
	"context"
	"errors"

	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/persistence"
	"github.com/gorenx/goren/session/projection"
	projectionactor "github.com/gorenx/goren/session/projection/actor"
	"github.com/gorenx/goren/session/projectioncache"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// reader owns one durable reconstruction and exits after replying.
type reader struct {
	identifier  session.SessionID
	backend     projectioncache.DurableEventReader
	definitions *goakt.GrainIdentity
	reply       *goakt.GrainReply
	initial     *projectioncache.CheckpointRecord
	replay      *projectioncache.Replay
	page        persistence.EventSegment
}

type origin struct {
	seq int64
	err error
}

type pageRead struct {
	page persistence.EventSegment
	err  error
}

type pageRestored struct {
	retryable bool
	result    projection.RestoreResult
	err       error
}

func (*reader) PreStart(*goakt.Context) error { return nil }
func (owner *reader) PostStop(*goakt.Context) error {
	if owner.reply != nil {
		owner.reply.Response(ColdView{
			Err: errors.New("projection cache: reader stopped before completion"),
		})
	}
	return nil
}

func (owner *reader) Receive(rc *goakt.ReceiveContext) {
	switch input := rc.Message().(type) {
	case *goakt.PostStart:
		var rows projection.Checkpoint
		if owner.initial != nil {
			rows = owner.initial.Rows
		}
		ctx, self := context.WithoutCancel(rc.Context()), rc.Self()
		rc.RequestGrain(owner.definitions, projectionactor.RestoreFloor{
			Checkpoint: rows,
		}, goakt.WithRequestTimeout(0)).Then(func(payload any, failure error) {
			next := origin{
				err: failure,
			}
			if failure == nil {
				floor, ok := payload.(projectionactor.Floor)
				if !ok {
					next.err = errors.New("projection cache: invalid restore floor response")
				} else if floor.Seq != nil {
					next.seq = *floor.Seq
				}
			}
			if err := goakt.Tell(ctx, self, next); err != nil {
				self.ActorSystem().Logger().Errorf("projection cache: deliver restore floor: %v", err)
			}
		})
	case origin:
		if input.err != nil {
			owner.finish(rc, input.err)
			return
		}
		var err error
		owner.replay, err = projectioncache.NewReplay(owner.identifier, owner.initial, input.seq)
		if err != nil {
			owner.finish(rc, err)
			return
		}
		owner.readPage(rc)
	case pageRead:
		if input.err != nil {
			owner.finish(rc, input.err)
			return
		}
		if err := owner.replay.ValidatePage(input.page); err != nil {
			if errors.Is(err, projectioncache.ErrCheckpointIdentity) && owner.retry(rc) {
				return
			}
			owner.finish(rc, err)
			return
		}
		owner.page = input.page
		cursor, _ := owner.replay.Next()
		ctx, self := context.WithoutCancel(rc.Context()), rc.Self()
		rc.RequestGrain(owner.definitions, projectionactor.Restore{
			Checkpoint: owner.replay.Checkpoint(),
			Events:     input.page.Events,
			BaseSeq:    cursor.FromSeq,
		}, goakt.WithRequestTimeout(0)).Then(func(payload any, failure error) {
			next := pageRestored{
				err: failure,
			}
			if failure == nil {
				restored, ok := payload.(projectionactor.Restored)
				if !ok {
					next.err = errors.New("projection cache: invalid restore response")
				} else {
					next.result, next.err = restored.Result, restored.Err
					next.retryable = restored.Err != nil
				}
			}
			if err := goakt.Tell(ctx, self, next); err != nil {
				self.ActorSystem().Logger().Errorf("projection cache: deliver restored page: %v", err)
			}
		})
	case pageRestored:
		if input.err != nil {
			if input.retryable && owner.retry(rc) {
				return
			}
			owner.finish(rc, input.err)
			return
		}
		if err := owner.replay.Accept(owner.page, input.result); err != nil {
			owner.finish(rc, err)
			return
		}
		owner.page = persistence.EventSegment{}
		if _, more := owner.replay.Next(); more {
			owner.readPage(rc)
		} else {
			owner.finish(rc, nil)
		}
	default:
		rc.Unhandled()
	}
}

func (owner *reader) readPage(rc *goakt.ReceiveContext) {
	cursor, _ := owner.replay.Next()
	backend, identifier := owner.backend, owner.identifier
	ctx := context.WithoutCancel(rc.Context())
	if err := rc.Self().PipeTo(ctx, rc.Self(), func() (any, error) {
		page, err := backend.ReadEventsFrom(ctx, identifier, cursor)
		return pageRead{
			page: page,
			err:  err,
		}, nil
	}); err != nil {
		owner.finish(rc, err)
	}
}

func (owner *reader) retry(rc *goakt.ReceiveContext) bool {
	if owner.initial == nil {
		return false
	}
	owner.initial = nil
	owner.page = persistence.EventSegment{}
	var err error
	owner.replay, err = projectioncache.NewReplay(owner.identifier, nil, 0)
	if err != nil {
		owner.finish(rc, err)
	} else {
		owner.readPage(rc)
	}
	return true
}

func (owner *reader) finish(rc *goakt.ReceiveContext, failure error) {
	result := ColdView{
		Err: failure,
	}
	if failure == nil {
		value, err := owner.replay.Result()
		result.Err = err
		if err == nil {
			result.Value = &value
		}
	}
	owner.reply.Response(result)
	owner.reply = nil
	rc.Shutdown()
}
