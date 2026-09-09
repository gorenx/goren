package loopactor

import (
	"context"
	"errors"
	middlewareactor "github.com/gorenx/goren/agent/middleware/actor"
	"time"

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
	// All execution decisions live in business; these fields retain transport only.
	maintenance       *maintenance
	idle              chan struct{}
	stopped           chan struct{}
	business          *agentloop.ReactLoopAgent
	models            *llmactor.Client
	callID            llmactor.CallID
	generation        agentloop.GenerationReceiver
	chunk             agentloop.ChunkReceiver
	stream            agentloop.StreamReceiver
	modelCancellation *completeCancel
	calls             map[string]struct{}
	cancellations     map[string]cancellationRequest
	deliveries        int
	conversation      *goakt.GrainIdentity
	creationID        string
	instanceID        string
	initialization    *initialization
	prompt            *goakt.GrainIdentity
	modelRouter       *goakt.PID
	middleware        *goakt.GrainIdentity
	handlers          middlewareactor.Handlers
	tools             *goakt.GrainIdentity
	replyTimeout      time.Duration
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
	if owner.modelCancellation != nil {
		owner.modelCancellation.respond(errors.New("agentloop: stopped before model cancellation acknowledgement"))
		owner.modelCancellation = nil
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
	if owner.models != nil && owner.models.Receive(gc, llmactor.Receivers{
		Generation:   owner,
		Chunk:        owner,
		Close:        owner,
		Cancellation: owner,
	}) {
		return
	}
	if owner.receiveCapabilities(gc) || owner.receiveInitialization(gc) ||
		owner.receiveInbox(gc) || owner.receiveLifecycle(gc) {
		return
	}
	gc.Unhandled()
}
