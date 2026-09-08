package sessionactor

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/gorenx/goren/session"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

// admission retains one initialization until the creation authority replies.
// A prepared log has no storage effects and is not visible before authorization.
type admission struct {
	sessionID session.SessionID
	token     string
	// creationID fixes the requested grant and is installed with the log, never replaced by a later request.
	creationID string
	history    *session.Log
	storage    Storage
	recovery   RecoveryStorage
	reply      *goakt.GrainReply
	cancel     context.CancelFunc
}

type admissionFinished struct {
	token string
	err   error
}

func (owner *Grain) initialize(gc *goakt.GrainContext, input Initialize) {
	var err error
	switch {
	case owner.admission != nil || owner.restoring != nil:
		err = ErrRestoring
	case input.SessionID == "" || gc.Self().Name() != RuntimeName(string(input.SessionID)) || input.CreationID == "" || (owner.history != nil && owner.creationID != input.CreationID):
		err = ErrCreation
	case owner.closing != nil:
		err = session.ErrWritesClosed
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
	if input.Storage == nil {
		gc.Response(Initialized{
			Err: errors.New("session: storage is required"),
		})
		return
	}
	history, err := session.NewLog(input.SessionID, input.Options)
	if err != nil {
		gc.Response(Initialized{
			Err: err,
		})
		return
	}
	owner.beginAdmission(gc, input.SessionID, input.CreationID, history, input.Storage, nil, input.Authority)
}

func (owner *Grain) beginAdmission(
	gc *goakt.GrainContext, identifier session.SessionID, creationID string,
	history *session.Log, storage Storage, recovery RecoveryStorage, registry *goakt.GrainIdentity,
) {
	if err := gc.EnableReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))); err != nil {
		gc.Response(Initialized{
			Err: err,
		})
		return
	}
	if registry == nil {
		gc.Response(Initialized{
			Err: errors.New("session: creation authority is required"),
		})
		return
	}
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(Initialized{
			Err: errors.New("session: initialization requires an asynchronous request"),
		})
		return
	}
	token := uuid.NewString()
	ctx, cancel := context.WithCancel(context.WithoutCancel(gc.Context()))
	results := make(chan admissionFinished, 1)
	if err := gc.PipeToSelf(
		func() (any, error) {
			select {
			case result := <-results:
				return result, nil
			case <-ctx.Done():
				return admissionFinished{
					token: token,
					err:   context.Cause(ctx),
				}, nil
			}
		},
	); err != nil {
		cancel()
		reply.Response(Initialized{
			Err: err,
		})
		return
	}
	owner.admission = &admission{
		sessionID:  identifier,
		token:      token,
		creationID: creationID,
		history:    history,
		storage:    storage,
		recovery:   recovery,
		reply:      reply,
		cancel:     cancel,
	}
	gc.RequestGrain(registry, Claim{
		SessionID:  identifier,
		CreationID: creationID,
		InstanceID: owner.instanceID,
	}).Then(
		func(payload any, failure error) {
			if failure == nil {
				result, ok := payload.(Claimed)
				if !ok {
					failure = errors.New("session: invalid creation authorization reply")
				} else {
					failure = result.Err
				}
			}
			results <- admissionFinished{
				token: token,
				err:   failure,
			}
		},
	)
}

func (owner *Grain) finishAdmission(gc *goakt.GrainContext, result admissionFinished) {
	if owner.admission == nil || owner.admission.token != result.token {
		return
	}
	pending := owner.admission
	owner.admission = nil
	pending.cancel()
	if result.err != nil {
		pending.reply.Response(Initialized{
			Err: result.err,
		})
		return
	}
	if pending.recovery != nil {
		owner.resume(gc, pending)
		return
	}
	owner.history = pending.history
	owner.creationID = pending.creationID
	owner.storage = pending.storage
	owner.publishEvents(gc, owner.history.Snapshot().Events)
	owner.startWrite(gc)
	pending.reply.Response(Initialized{
		Header:  owner.history.Header(),
		Barrier: owner.history.Barrier(),
	})
}
