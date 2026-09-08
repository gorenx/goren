package loopactor

import (
	"context"
	"errors"

	llmactor "github.com/gorenx/goren/llm/actor"

	"github.com/google/uuid"
	"github.com/gorenx/goren/agentloop"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Grain schedules one Agent under its SessionID framework name.
// Business state and decisions belong to the Agent implementation.
// Session requests stash ordinary messages, so committed changes are applied
// before another Inbox command can prepare a mutation.
type Grain struct {
	// idle closes when the current busy interval ends; waiters live in framework tasks.
	maintenance    *maintenance
	idle           chan struct{}
	stopped        chan struct{}
	opening        *turnOpening
	stopping       *stoppingInvocation
	preparation    *stepPreparation
	business       *agentloop.ReactLoopAgent
	ending         *turnClosure
	model          *modelInvocation
	conversation   *goakt.GrainIdentity
	creationID     string
	instanceID     string
	initialization *initialization
	prompt         *goakt.GrainIdentity
}

// OnActivate starts without a Session binding or mutable business dependencies.
func (owner *Grain) OnActivate(context.Context, *goakt.GrainProps) error {
	owner.instanceID = uuid.NewString()
	owner.stopped = make(chan struct{})
	owner.business = &agentloop.ReactLoopAgent{}
	return nil
}

// OnDeactivate releases local projections; Session business close is independent.
func (owner *Grain) OnDeactivate(ctx context.Context, _ *goakt.GrainProps) error {
	close(owner.stopped)
	if owner.opening != nil {
		owner.finishTurnStart(owner.opening, false, errors.New("agentloop: stopped during Turn start"))
	}
	if owner.stopping != nil {
		owner.finishStopping(owner.stopping, turnContinued{
			err: errors.New("agentloop: stopped during stopping hook"),
		})
	}
	if owner.preparation != nil {
		owner.finishPreparation(owner.preparation, false, errors.New("agentloop: stopped during Step preparation"))
	}
	if owner.model != nil {

		if owner.model.canceling != nil {
			owner.model.canceling.completion.reply.Response(Changed{
				Err: errors.New("agentloop: stopped before LLM cancellation acknowledgement"),
			})
			owner.model.canceling = nil
		}
		owner.finishModel(owner.model, errors.New("agentloop: stopped during model attempt"))
		owner.model = nil
	}
	if owner.ending != nil {
		owner.finishTurnRequest(owner.ending, errors.New("agentloop: stopped during Turn settlement"))
		owner.ending = nil
	}
	if owner.initialization != nil {
		owner.finishInitialization(owner.initialization, errors.New("agentloop: stopped during initialization"))
	}
	owner.prompt = nil
	owner.conversation = nil
	owner.creationID = ""
	owner.business = nil
	return nil
}

// OnReceive only dispatches commands and completions to their runtime handlers.
func (owner *Grain) OnReceive(gc *goakt.GrainContext) {
	defer owner.refreshIdle()
	if owner.model != nil && owner.model.models != nil && owner.model.models.Receive(gc, llmactor.Receivers{
		Generation:   owner,
		Chunk:        owner,
		Close:        owner,
		Cancellation: owner,
	}) {
		return
	}
	if owner.receiveInitialization(gc) ||
		owner.receiveInbox(gc) ||
		owner.receiveTurn(gc) ||
		owner.receiveStep(gc) ||
		owner.receiveModel(gc) ||
		owner.receiveLifecycle(gc) {
		return
	}
	gc.Unhandled()
}
