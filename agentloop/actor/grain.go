package loopactor

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/gorenx/goren/agent"
	agentactor "github.com/gorenx/goren/agent/actor"
	middlewareactor "github.com/gorenx/goren/agent/middleware/actor"
	"github.com/gorenx/goren/agentloop"
	llmactor "github.com/gorenx/goren/llm/actor"
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
	// calls retains transport progress until Tool Runtime confirms release.
	// Only active calls are cancellation targets; releasing calls remain busy.
	calls map[string]toolCallPhase
	// cancellations maps a capability request ID to its target and cancellation
	// message, removed only when that request delivers its final response.
	cancellations  map[string]cancellationRequest
	deliveries     int
	conversation   *goakt.GrainIdentity
	creationID     string
	instanceID     string
	initialization *initialization
	prompt         *goakt.GrainIdentity
	modelRouter    *goakt.PID
	middleware     *goakt.GrainIdentity
	handlers       middlewareactor.Handlers
	tools          *goakt.GrainIdentity
	replyTimeout   time.Duration
}

// RuntimeName returns this service's stable address within a Session lifetime.
// Business identity is carried explicitly in messages, never parsed from this name.
func RuntimeName(identifier string) string { return identifier + ".agentloop" }

func (owner *Grain) checkCreation(identifier string) error {
	if identifier == "" || owner.creationID != identifier {
		return errors.New("agentloop: creation is not authorized or current")
	}
	return nil
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
	previous := owner.business.StatusValue()
	defer func() {
		owner.refreshIdle()
		owner.publishStatus(gc, previous)
	}()
	if owner.models != nil && owner.models.Receive(gc, llmactor.Receivers{
		Generation:   owner,
		Chunk:        owner,
		Close:        owner,
		Cancellation: owner,
	}) {
		return
	}
	if owner.receiveDelivery(gc) || owner.receiveInitialization(gc) ||
		owner.receiveInbox(gc) {
		return
	}
	switch input := gc.Message().(type) {
	case agentactor.BeginMaintenance:
		owner.beginMaintenance(gc, input)
	case agentactor.FinishMaintenance:
		owner.finishMaintenance(gc, input)
	case completeCancel:
		owner.finishCancellation(gc, input)
	case Cancel:
		owner.cancel(gc, input)
	case WhenIdle:
		owner.whenIdle(gc, input)
	case idleObserved:
		owner.confirmIdle(gc, input)
	case agentactor.Verify:
		failure := owner.checkCreation(input.CreationID)
		if failure == nil && (input.InstanceID == "" || input.InstanceID != owner.instanceID) {
			failure = errors.New("agentloop: activation is not current")
		}
		gc.Response(agentactor.Verified{
			Err: failure,
		})
	case agentactor.PrepareClose:
		owner.requestClosePreparation(gc, input)
	case closePreparation:
		owner.confirmClosePreparation(gc, input)
	case agentactor.CloseResources:
		owner.closeResources(gc, input)
	default:
		gc.Unhandled()
	}
}

func (owner *Grain) services(gc *goakt.GrainContext) agentloop.Services {
	scope := serviceScope{
		owner: owner,
		gc:    gc,
	}
	return agentloop.Services{
		Conversation: sessionPort{scope: scope},
		Prompts:      promptPort{scope: scope},
		Middleware:   middlewarePort{scope: scope},
		Models:       modelPort{scope: scope},
		Tools:        toolPort{scope: scope},
		Observer:     scope,
	}
}

func (owner *Grain) receiveDelivery(gc *goakt.GrainContext) bool {
	input, ok := gc.Message().(capabilityDelivery)
	if !ok {
		return false
	}
	if owner.checkCreation(input.creationID) != nil {
		return true
	}
	owner.deliveries--
	delete(owner.cancellations, input.id)
	input.accept(owner.services(gc))
	return true
}

// publishStatus reports business transitions without maintaining another state machine.
func (owner *Grain) publishStatus(gc *goakt.GrainContext, previous agent.Status) {
	current := owner.business.StatusValue()
	if current == previous || owner.business.ID() == "" {
		return
	}
	publishStatus(gc.Context(), gc.ActorSystem(), agent.StatusNotice{
		AgentID: owner.business.ID(),
		Status:  current,
	})
}

func publishStatus(ctx context.Context, system goakt.ActorSystem, notice agent.StatusNotice) {
	target := system.TopicActor()
	if target == nil {
		return
	}
	err := goakt.Tell(ctx, target, goakt.NewPublish(uuid.NewString(), agent.StatusTopic, notice))
	if err != nil {
		system.Logger().Error(err)
	}
}
