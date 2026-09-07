package sessionactor

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/persistence"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// ErrRestoring means one initialization task still owns its completion.
var ErrRestoring = errors.New("session: recovery is in progress")

type restoration struct {
	token      string
	creationID string
	storage    RecoveryStorage
	reply      *goakt.GrainReply
	cancel     context.CancelFunc
}

type restorationFinished struct {
	token   string
	history *session.Log
	durable int64
	err     error
}

func (owner *Grain) startResume(gc *goakt.GrainContext, input Resume) {
	var err error
	switch {
	case owner.restoring != nil:
		err = ErrRestoring
	case input.CreationID == "" || (owner.history != nil && input.CreationID != owner.creationID):
		err = ErrCreation
	case owner.closing != nil:
		err = session.ErrWritesClosed
	case input.Storage == nil:
		err = errors.New("session: recovery storage is required")
	}
	if err != nil {
		gc.Response(Initialized{
			Err: err,
		})
		return
	}
	if owner.history != nil {
		gc.Response(Initialized{
			Header:  owner.history.Header(),
			Barrier: owner.history.Barrier(),
		})
		return
	}
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(Initialized{
			Err: errors.New("session: resume requires an asynchronous request"),
		})
		return
	}
	ctx, cancel := context.WithCancel(gc.Context())
	token := uuid.NewString()
	identifier := session.SessionID(gc.Self().Name())
	storage := input.Storage
	owner.restoring = &restoration{
		token:      token,
		creationID: input.CreationID,
		storage:    storage,
		reply:      reply,
		cancel:     cancel,
	}
	err = gc.PipeToSelf(func() (payload any, taskErr error) {
		defer func() {
			if recovered := recover(); recovered != nil {
				payload = restorationFinished{
					token: token,
					err:   fmt.Errorf("session: recovery panicked: %v", recovered),
				}
			}
		}()
		history, durable, err := restore(ctx, identifier, storage)
		return restorationFinished{
			token:   token,
			history: history,
			durable: durable,
			err:     err,
		}, nil
	})
	if err != nil {
		cancel()
		owner.restoring = nil
		reply.Response(Initialized{
			Err: err,
		})
	}
}

func (owner *Grain) finishResume(gc *goakt.GrainContext, result restorationFinished) {
	if owner.restoring == nil || owner.restoring.token != result.token {
		return
	}
	pending := owner.restoring
	owner.restoring = nil
	pending.cancel()
	if result.err != nil {
		pending.reply.Response(Initialized{
			Err: result.err,
		})
		return
	}
	owner.history = result.history
	owner.durable = result.durable
	owner.creationID = pending.creationID
	owner.storage = pending.storage
	pending.reply.Response(Initialized{
		Header:  owner.history.Header(),
		Barrier: owner.history.Barrier(),
	})
	owner.startWrite(gc)
}

func restore(ctx context.Context, identifier session.SessionID, storage RecoveryStorage) (*session.Log, int64, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		stored, err := storage.Load(ctx, identifier)
		if err != nil {
			return nil, 0, err
		}
		if stored == nil {
			return nil, 0, &persistence.NotFoundError{ID: identifier}
		}
		recovery, err := persistence.Recover(identifier, *stored)
		if err != nil {
			return nil, 0, err
		}
		revision, err := storage.Revision(ctx, identifier)
		if err != nil {
			return nil, 0, err
		}
		if revision == nil || *revision != stored.Revision {
			continue
		}
		if recovery.Repair.Marker != nil || len(recovery.Repair.ClosingEvents) != 0 {
			if err := storage.CommitRepair(ctx, recovery.Repair); err != nil {
				return nil, 0, err
			}
			continue
		}
		history, err := session.NewLog(identifier, recovery.Options)
		if err != nil {
			return nil, 0, err
		}
		return history, int64(len(stored.Events)), nil
	}
}
