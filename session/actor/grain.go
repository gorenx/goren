package sessionactor

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/gorenx/goren/session"
	goakt "github.com/tochemey/goakt/v4/actor"
)

var (
	// ErrUnavailable means the logical identity has not been initialized.
	ErrUnavailable = errors.New("session: Grain has not been initialized")
	// ErrCreation identifies a command from another or absent creation attempt.
	ErrCreation = errors.New("session: creation identity mismatch")
	// ErrBarrier rejects a foreign, invalid, or not yet committed prefix.
	ErrBarrier = errors.New("session: requested prefix is unavailable")
)

// Grain owns one Session log under a stable framework name equal to SessionID.
// It has no Store lifecycle, mailbox substitute, or mutable Agent reference.
type Grain struct {
	// instanceID is generated on each OnActivate to distinguish activations of one logical Grain.
	// Registry binds the creation grant to it so stale initialization cannot regain authority.
	instanceID string
	// creationID identifies the business creation owning this log, installed after authorization
	// and initialization. Commit/Read/Flush/Close must match it to reject earlier creations.
	creationID string
	admission  *admission
	history    *session.Log
	storage    Storage
	durable    int64
	writing    *write
	writeErr   error
	flushes    []flush
	closing    *shutdown
	restoring  *restoration
}

// OnActivate does not grant business creation authority.
func (owner *Grain) OnActivate(context.Context, *goakt.GrainProps) error {
	owner.instanceID = uuid.NewString()
	return nil
}

// OnDeactivate cancels in-flight work and fails deferred requests on forced shutdown.
// It does not close the shared storage adapter or acknowledge normal business drain.
func (owner *Grain) OnDeactivate(context.Context, *goakt.GrainProps) error {
	if owner.admission != nil {
		owner.admission.cancel()
		owner.admission.reply.Response(Initialized{Err: ErrUnavailable})
		owner.admission = nil
	}
	if owner.restoring != nil {
		owner.restoring.cancel()
		owner.restoring.reply.Response(Initialized{
			Err: ErrUnavailable,
		})
		owner.restoring = nil
	}
	if owner.writing != nil {
		owner.writing.cancel()
	}
	owner.finishFlushes(ErrUnavailable)
	owner.finishClose(ErrUnavailable)
	return nil
}

// OnReceive serializes short memory operations. Persistence is not implied by replies.
func (owner *Grain) OnReceive(gc *goakt.GrainContext) {
	switch incoming := gc.Message().(type) {
	case Resume:
		owner.startResume(gc, incoming)
	case restorationFinished:
		owner.finishResume(gc, incoming)
	case Initialize:
		owner.initialize(gc, incoming)
	case admissionFinished:
		owner.finishAdmission(gc, incoming)
	case Commit:
		if err := owner.authorize(incoming.CreationID); err != nil {
			gc.Response(Committed{
				Err: err,
			})
			return
		}
		if owner.closing != nil {
			gc.Response(Committed{
				Err: session.ErrWritesClosed,
			})
			return
		}
		result, err := owner.history.Commit(gc.Context(), incoming.Plan)
		gc.Response(Committed{
			Result: result,
			Err:    err,
		})
		if err == nil {
			owner.publishEvents(gc, result.Events)
			owner.startWrite(gc)
		}
	case Close:
		owner.startClose(gc, incoming)
	case Flush:
		owner.startFlush(gc, incoming)
	case writeFinished:
		owner.completeWrite(gc, incoming)
	case Read:
		if err := owner.authorize(incoming.CreationID); err != nil {
			gc.Response(View{
				Err: err,
			})
			return
		}
		current := owner.history.Barrier()
		if minimum := incoming.Minimum; minimum != nil &&
			(minimum.SessionID != current.SessionID || minimum.NextSeq < 0 || minimum.NextSeq > current.NextSeq) {
			gc.Response(View{
				Err: ErrBarrier,
			})
			return
		}
		gc.Response(View{
			Snapshot: owner.history.Snapshot(),
		})
	default:
		gc.Unhandled()
	}
}

func (owner *Grain) authorize(identifier string) error {
	if owner.history == nil {
		return ErrUnavailable
	}
	if identifier == "" || identifier != owner.creationID {
		return ErrCreation
	}
	return nil
}
