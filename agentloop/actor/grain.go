package loopactor

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/gorenx/goren/agent"
	agentactor "github.com/gorenx/goren/agent/actor"
	"github.com/gorenx/goren/agentloop"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Grain schedules one Agent under its SessionID framework name.
// Business state and decisions belong to the Agent implementation.
// Session requests stash ordinary messages, so committed changes are applied
// before another Inbox command can prepare a mutation.
type Grain struct {
	// idle closes when the current busy interval ends; waiters live in framework tasks.
	idle            chan struct{}
	stopped         chan struct{}
	opening         *turnOpening
	stopping        *stoppingInvocation
	preparation     *stepPreparation
	business        *agentloop.ReactLoopAgent
	ending          *turnClosure
	model           *modelAttempt
	conversation    *goakt.GrainIdentity
	creationID      string
	instanceID      string
	initialization  *initialization
	sessionRegistry *goakt.GrainIdentity
	prompt          *goakt.GrainIdentity
	promptClosing   *promptClosure
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
		owner.model.cancelRead(agent.NewCancellation(agent.DisposedCancel{}))
		if owner.model.reader != nil {
			_ = goakt.Tell(ctx, owner.model.reader, new(goakt.PoisonPill))
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
	if owner.promptClosing != nil {
		owner.promptClosing.cancel()
		owner.promptClosing.reply.Response(agentactor.Closed{
			Err: errors.New("agentloop: stopped during Prompt close"),
		})
		owner.promptClosing = nil
	}
	owner.prompt = nil
	owner.conversation = nil
	owner.creationID = ""
	owner.sessionRegistry = nil
	owner.business = nil
	return nil
}

// OnReceive only dispatches commands and completions to their runtime handlers.
func (owner *Grain) OnReceive(gc *goakt.GrainContext) {
	defer owner.refreshIdle()
	switch input := gc.Message().(type) {
	case startTurn:
		owner.startTurn(gc)
	case confirmTurnStart:
		owner.confirmTurnStart(gc, input)
	case continueTurn:
		owner.continueTurn(gc, input)
	case stoppingCompleted:
		owner.completeStopping(input)
	case prepareStep:
		owner.startStep(gc, input)
	case inputRecovered:
		owner.reconcileInput(gc, input)
	case inputClaimed:
		owner.claimConfirmed(gc, input)
	case contextRead:
		owner.preparePrompt(gc, input)
	case promptAssembled:
		owner.resolveStepAdmission(gc, input)
	case admissionResolved:
		owner.openStep(gc, input)
	case stepRecovered:
		owner.reconcileStep(gc, input)
	case stepStarted:
		owner.stepConfirmed(gc, input)
	case messageCommitted:
		owner.messageConfirmed(gc, input)
	case preparationFailed:
		owner.finishPreparation(input.operation, false, input.err)
	case Initialize:
		owner.initialize(gc, input)
	case initializationAuthorized:
		owner.startSession(gc, input)
	case initializationSession:
		owner.restoreInbox(gc, input)
	case initializationRestored:
		owner.initializeModel(gc, input)
	case initializationModel:
		owner.initializePrompt(gc, input)
	case GetInbox:
		owner.readInbox(gc)
	case Followup:
		owner.followup(gc, input)
	case Steer:
		owner.steer(gc, input)
	case Inject:
		owner.inject(gc, input)
	case Replace:
		owner.replace(gc, input)
	case Remove:
		owner.remove(gc, input)
	case completeCancel:
		owner.finishCancellation(gc, input)
	case Cancel:
		owner.cancel(gc, input)
	case WhenIdle:
		owner.whenIdle(gc, input)
	case idleObserved:
		owner.confirmIdle(gc, input)
	case requestModel:
		owner.requestModel(gc, input)
	case streamProgress:
		owner.advanceStream(gc, input)
	case finishTurn:
		owner.settleTurn(gc, input)
	case turnProgress:
		owner.advanceTurnEnd(gc, input)
	case agentactor.PrepareClose:
		gc.Response(agentactor.Prepared{
			Err: owner.prepareClose(input.CreationID),
		})
	case agentactor.CloseResources:
		owner.closeResources(gc, input)
	case promptStopped:
		owner.finishPromptClose(gc, input)
	default:
		gc.Unhandled()
	}
}
