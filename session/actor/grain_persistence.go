package sessionactor

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/google/uuid"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/persistence"
	goakt "github.com/tochemey/goakt/v4/actor"
)

type write struct {
	token   string
	nextSeq int64
	cancel  context.CancelFunc
}

type writeFinished struct {
	token string
	err   error
}

type flush struct {
	barrier session.WriteBarrier
	reply   *goakt.GrainReply
}

func (owner *Grain) startFlush(gc *goakt.GrainContext, input Flush) {
	err := owner.authorize(input.CreationID)
	if err == nil {
		current := owner.history.Barrier()
		if input.Barrier.SessionID != current.SessionID || input.Barrier.NextSeq < 0 || input.Barrier.NextSeq > current.NextSeq {
			err = ErrBarrier
		}
	}
	if err != nil {
		gc.Response(Flushed{
			Err: err,
		})
		return
	}
	if owner.durable >= input.Barrier.NextSeq {
		gc.Response(Flushed{
			Barrier: owner.durableBarrier(),
		})
		return
	}
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(Flushed{
			Err: errors.New("session: flush requires an asynchronous request"),
		})
		return
	}
	owner.flushes = append(owner.flushes, flush{
		barrier: input.Barrier,
		reply:   reply,
	})
	owner.startWrite(gc)
}

func (owner *Grain) startWrite(gc *goakt.GrainContext) {
	if owner.writing != nil || owner.durable >= owner.history.Seq() {
		return
	}
	captured := owner.history.Snapshot()
	header := owner.history.Header()
	storage := owner.storage
	firstSeq := owner.durable
	reconcile := firstSeq == 0 || owner.writeErr != nil
	ctx, cancel := context.WithCancel(context.WithoutCancel(gc.Context()))
	token := uuid.NewString()
	owner.writing = &write{
		token:   token,
		nextSeq: captured.Barrier.NextSeq,
		cancel:  cancel,
	}
	err := gc.PipeToSelf(func() (payload any, taskErr error) {
		result := writeFinished{
			token: token,
		}
		defer func() {
			if recovered := recover(); recovered != nil {
				payload = writeFinished{
					token: token,
					err:   fmt.Errorf("session: storage operation panicked: %v", recovered),
				}
			}
		}()
		result.err = persist(ctx, storage, header, captured.Events, firstSeq, reconcile)
		return result, nil
	})
	if err != nil {
		cancel()
		owner.writing = nil
		owner.writeErr = err
		owner.finishFlushes(err)
		owner.finishClose(err)
	}
}

func (owner *Grain) completeWrite(gc *goakt.GrainContext, result writeFinished) {
	if owner.writing == nil || owner.writing.token != result.token {
		return
	}
	if result.err == nil {
		owner.durable = owner.writing.nextSeq
	}
	owner.writing.cancel()
	owner.writing = nil
	owner.writeErr = result.err
	owner.finishFlushes(result.err)
	if result.err == nil {
		owner.startWrite(gc)
	}
	owner.finishClose(result.err)
}

func (owner *Grain) durableBarrier() session.WriteBarrier {
	return session.WriteBarrier{
		SessionID: owner.history.Header().ID,
		NextSeq:   owner.durable,
	}
}

func (owner *Grain) finishFlushes(err error) {
	pending := owner.flushes[:0]
	for _, request := range owner.flushes {
		if err == nil && request.barrier.NextSeq > owner.durable {
			pending = append(pending, request)
			continue
		}
		request.reply.Response(Flushed{
			Barrier: owner.durableBarrier(),
			Err:     err,
		})
	}
	clear(owner.flushes[len(pending):])
	owner.flushes = pending
}

// persist writes detached values only. Reconciliation handles an uncertain prior commit.
func persist(ctx context.Context, backend Storage, metadata session.Header, entries []session.Event, firstSeq int64, reconcile bool) error {
	materialize := firstSeq == 0
	if reconcile {
		stored, err := backend.Load(ctx, metadata.ID)
		if err != nil {
			return err
		}
		if stored == nil {
			if firstSeq != 0 {
				return errors.New("session: durable prefix disappeared")
			}
		} else {
			if stored.Marker != nil || !reflect.DeepEqual(stored.Header, metadata) || len(stored.Events) > len(entries) {
				return errors.New("session: durable log requires recovery or belongs to another creation")
			}
			for index, entry := range stored.Events {
				if !reflect.DeepEqual(entry, entries[index]) {
					return errors.New("session: durable prefix differs from committed memory")
				}
			}
			if int64(len(stored.Events)) < firstSeq {
				return errors.New("session: durable prefix regressed")
			}
			firstSeq = int64(len(stored.Events))
			materialize = false
		}
	}
	if firstSeq == int64(len(entries)) {
		return nil
	}
	return backend.AppendBatch(ctx, persistence.EventBatch{
		Header:      metadata,
		Events:      entries[firstSeq:],
		Materialize: materialize,
	})
}
