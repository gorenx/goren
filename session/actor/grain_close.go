package sessionactor

import (
	"errors"

	"github.com/gorenx/goren/session"
	goakt "github.com/tochemey/goakt/v4/actor"
)

type shutdown struct {
	barrier session.WriteBarrier
	replies []*goakt.GrainReply
}

func (owner *Grain) startClose(gc *goakt.GrainContext, input Close) {
	if input.SessionID == "" || gc.Self().Name() != RuntimeName(string(input.SessionID)) {
		gc.Response(Closed{
			Err: ErrCreation,
		})
		return
	}

	if owner.history == nil {
		owner.closeEmpty(gc, input)
		return
	}
	if err := owner.authorize(input.CreationID); err != nil {
		gc.Response(Closed{
			Err: err,
		})
		return
	}
	barrier := owner.history.Barrier()
	if owner.closing != nil {
		barrier = owner.closing.barrier
	}
	if owner.writing == nil && owner.durable >= barrier.NextSeq {
		if owner.closing == nil {
			owner.closing = &shutdown{
				barrier: barrier,
			}
		}
		owner.storage = nil
		gc.Response(Closed{
			Barrier: owner.durableBarrier(),
		})
		return
	}
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(Closed{
			Err: errors.New("session: close requires an asynchronous request"),
		})
		return
	}
	if owner.closing == nil {
		owner.closing = &shutdown{
			barrier: barrier,
		}
	}
	owner.closing.replies = append(owner.closing.replies, reply)
	owner.startWrite(gc)
}

func (owner *Grain) finishClose(err error) {
	if owner.closing == nil {
		return
	}
	if err == nil {
		if owner.writing != nil || owner.durable < owner.closing.barrier.NextSeq {
			return
		}
		owner.storage = nil
	}
	for _, reply := range owner.closing.replies {
		reply.Response(Closed{
			Barrier: owner.durableBarrier(),
			Err:     err,
		})
	}
	owner.closing.replies = nil
}
