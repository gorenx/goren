package agentloop

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

type activityKind uint8

const (
	activityIdle activityKind = iota
	activityMaintenance
	activityRunning
)

type activityState struct {
	kind           activityKind
	lastTurn       int64
	turn           int64
	step           int64
	requestContext context.Context
	cancel         context.CancelCauseFunc
	cancelCause    agent.CancelCause
	wakeRequested  bool
}

type activityPosition struct {
	turn int64
	step int64
}

// activityCoordinator is the single admission and convergence owner for one
// Agent. It does not build requests, run Tools, or append Turn facts.
type activityCoordinator struct {
	subject *ReactLoopAgent
	pending *agent.Inbox
	events  *agentEventPublisher
	turns   *turnRunner

	mutex        sync.Mutex
	state        activityState
	activityDone chan struct{}
	accepting    bool
	disposed     bool
}

func newActivityCoordinator(
	subject *ReactLoopAgent,
	pending *agent.Inbox,
	events *agentEventPublisher,
	lastTurn int64,
) *activityCoordinator {
	closedActivity := make(chan struct{})
	close(closedActivity)
	return &activityCoordinator{
		subject: subject,
		pending: pending,
		events:  events,
		state: activityState{
			kind:     activityIdle,
			lastTurn: lastTurn,
		},
		activityDone: closedActivity,
	}
}

func (coordinator *activityCoordinator) beginServing() error {
	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	if coordinator.disposed {
		return fmt.Errorf(
			"agentloop: Agent %q is disposed",
			coordinator.subject.identifier,
		)
	}
	if coordinator.accepting {
		return fmt.Errorf(
			"agentloop: Agent %q is already live",
			coordinator.subject.identifier,
		)
	}
	coordinator.accepting = true
	return nil
}

func (coordinator *activityCoordinator) status() agent.Status {
	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	if coordinator.state.kind == activityRunning {
		return agent.StatusRunning
	}
	return agent.StatusIdle
}

func (coordinator *activityCoordinator) send(
	input llm.UserMessage,
	target agent.InboxTarget,
	wakeup bool,
) error {
	coordinator.mutex.Lock()
	if coordinator.disposed || !coordinator.accepting {
		coordinator.mutex.Unlock()
		return fmt.Errorf(
			"agentloop: Agent %q is not live",
			coordinator.subject.identifier,
		)
	}
	wakingAfterAbort := wakeup &&
		coordinator.state.kind != activityIdle &&
		coordinator.state.requestContext != nil &&
		coordinator.state.requestContext.Err() != nil
	coordinator.mutex.Unlock()

	resolvedTarget := target
	if wakingAfterAbort {
		resolvedTarget = agent.NextTurn
	}
	if err := coordinator.pending.Append(resolvedTarget, input); err != nil {
		return err
	}
	if wakeup {
		return coordinator.wake(wakingAfterAbort)
	}
	return nil
}

func (coordinator *activityCoordinator) cancel(
	cause agent.CancelCause,
	options agent.CancelOptions,
) {
	if cause == nil {
		cause = agent.UserCancel{}
	}
	if !options.KeepInbox {
		if err := coordinator.pending.Clear(); err != nil {
			coordinator.events.reportFailure(fmt.Errorf(
				"agentloop: Agent %q clear Inbox during cancel: %w",
				coordinator.subject.identifier,
				err,
			))
		}
	}
	coordinator.mutex.Lock()
	if coordinator.accepting && coordinator.state.kind != activityIdle {
		if !options.KeepInbox {
			coordinator.state.wakeRequested = false
		}
		if coordinator.state.cancelCause == nil {
			coordinator.state.cancelCause = cause
		}
		if coordinator.state.cancel != nil {
			coordinator.state.cancel(agentCancellation{
				cause: coordinator.state.cancelCause,
			})
		}
	}
	coordinator.mutex.Unlock()
}

func (coordinator *activityCoordinator) beginDispose() {
	coordinator.mutex.Lock()
	if coordinator.disposed {
		coordinator.mutex.Unlock()
		return
	}
	coordinator.disposed = true
	coordinator.accepting = false
	if coordinator.state.kind != activityIdle {
		coordinator.state.wakeRequested = false
		if coordinator.state.cancelCause == nil {
			coordinator.state.cancelCause = agent.DisposedCancel{}
		}
		if coordinator.state.cancel != nil {
			coordinator.state.cancel(agentCancellation{
				cause: coordinator.state.cancelCause,
			})
		}
	}
	coordinator.mutex.Unlock()
	if err := coordinator.pending.Clear(); err != nil {
		coordinator.events.reportFailure(fmt.Errorf(
			"agentloop: Agent %q clear Inbox during disposal: %w",
			coordinator.subject.identifier,
			err,
		))
	}
}

func (coordinator *activityCoordinator) whenIdle(
	requestContext context.Context,
) error {
	if requestContext == nil {
		return errors.New("agentloop: WhenIdle Context is nil")
	}
	for {
		coordinator.mutex.Lock()
		done := coordinator.activityDone
		idle := coordinator.state.kind == activityIdle &&
			!coordinator.state.wakeRequested
		coordinator.mutex.Unlock()
		if idle {
			return nil
		}
		select {
		case <-done:
		case <-requestContext.Done():
			return context.Cause(requestContext)
		}
	}
}

func (coordinator *activityCoordinator) runMaintenance(
	requestContext context.Context,
	operation func(context.Context) error,
) error {
	if requestContext == nil || operation == nil {
		return errors.New(
			"agentloop: maintenance Context and operation are required",
		)
	}
	operationContext, cancelActivity := context.WithCancelCause(requestContext)
	done := make(chan struct{})
	coordinator.mutex.Lock()
	if coordinator.disposed || !coordinator.accepting {
		coordinator.mutex.Unlock()
		cancelActivity(nil)
		return fmt.Errorf(
			"agentloop: Agent %q is not live",
			coordinator.subject.identifier,
		)
	}
	if coordinator.state.kind != activityIdle ||
		coordinator.state.wakeRequested {
		coordinator.mutex.Unlock()
		cancelActivity(nil)
		return fmt.Errorf(
			"agentloop: Agent %q already has active or queued work",
			coordinator.subject.identifier,
		)
	}
	lastTurn := coordinator.state.lastTurn
	coordinator.state = activityState{
		kind:           activityMaintenance,
		lastTurn:       lastTurn,
		requestContext: operationContext,
		cancel:         cancelActivity,
	}
	coordinator.activityDone = done
	coordinator.mutex.Unlock()
	defer func() {
		cancelActivity(nil)
		coordinator.finishActivity(done, lastTurn, false)
	}()
	return operation(operationContext)
}

func (coordinator *activityCoordinator) wake(wakingAfterAbort bool) error {
	coordinator.mutex.Lock()
	if coordinator.disposed || !coordinator.accepting {
		coordinator.mutex.Unlock()
		return fmt.Errorf(
			"agentloop: Agent %q is not live",
			coordinator.subject.identifier,
		)
	}
	if coordinator.turns == nil {
		coordinator.mutex.Unlock()
		return errors.New("agentloop: Agent Turn runner is unavailable")
	}
	if coordinator.state.kind != activityIdle {
		disposedCause := coordinator.state.cancelCause != nil &&
			coordinator.state.cancelCause.CancelKind() ==
				(agent.DisposedCancel{}).CancelKind()
		if !disposedCause &&
			(coordinator.state.kind == activityMaintenance ||
				wakingAfterAbort) {
			coordinator.state.wakeRequested = true
		}
		coordinator.mutex.Unlock()
		return nil
	}
	driverContext, cancelActivity := context.WithCancelCause(
		context.Background(),
	)
	initiated, err := agent.WithInitiator(driverContext, coordinator.subject)
	if err != nil {
		coordinator.mutex.Unlock()
		cancelActivity(nil)
		return err
	}
	done := make(chan struct{})
	lastTurn := coordinator.state.lastTurn
	coordinator.state = activityState{
		kind:           activityRunning,
		lastTurn:       lastTurn,
		turn:           lastTurn,
		requestContext: initiated,
		cancel:         cancelActivity,
	}
	coordinator.activityDone = done
	coordinator.mutex.Unlock()
	coordinator.events.publishStatus(agent.StatusRunning)
	go coordinator.drive(done)
	return nil
}

func (coordinator *activityCoordinator) drive(done chan struct{}) {
	for {
		coordinator.mutex.Lock()
		requestContext := coordinator.state.requestContext
		turns := coordinator.turns
		coordinator.mutex.Unlock()
		continueDriving, _ := turns.runTurn(requestContext)
		if !continueDriving {
			break
		}
	}
	coordinator.mutex.Lock()
	turn := coordinator.state.turn
	if coordinator.state.cancel != nil {
		coordinator.state.cancel(nil)
	}
	coordinator.mutex.Unlock()
	coordinator.finishActivity(done, turn, true)
}

// finishActivity preserves a latched wake until the successor activity has
// reserved its completion channel. WhenIdle therefore cannot observe a
// transient idle state between logically connected activities.
func (coordinator *activityCoordinator) finishActivity(
	done chan struct{},
	lastTurn int64,
	publishIdle bool,
) {
	coordinator.mutex.Lock()
	wakeRequested := coordinator.state.wakeRequested
	disposed := coordinator.disposed
	coordinator.state = activityState{
		kind:          activityIdle,
		lastTurn:      lastTurn,
		wakeRequested: wakeRequested,
	}
	coordinator.mutex.Unlock()
	if publishIdle && !disposed {
		coordinator.events.publishStatus(agent.StatusIdle)
	}
	if wakeRequested && coordinator.pending.HasPending() {
		if err := coordinator.wake(false); err != nil {
			coordinator.events.reportFailure(err)
		}
	}
	coordinator.mutex.Lock()
	if coordinator.state.kind == activityIdle {
		coordinator.state.wakeRequested = false
	}
	coordinator.mutex.Unlock()
	close(done)
}

func (coordinator *activityCoordinator) proposedTurn() int64 {
	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	return coordinator.state.turn + 1
}

func (coordinator *activityCoordinator) acceptTurn(turn int64) {
	coordinator.mutex.Lock()
	coordinator.state.turn = turn
	coordinator.mutex.Unlock()
}

func (coordinator *activityCoordinator) proposedStep() (int64, int64) {
	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	return coordinator.state.step + 1, coordinator.state.step
}

func (coordinator *activityCoordinator) acceptStep(step int64) {
	coordinator.mutex.Lock()
	coordinator.state.step = step
	coordinator.mutex.Unlock()
}

func (coordinator *activityCoordinator) snapshotPosition() activityPosition {
	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	return activityPosition{
		turn: coordinator.state.turn,
		step: coordinator.state.step,
	}
}

func (coordinator *activityCoordinator) renewTurnContext(
	previous context.Context,
) (bool, error) {
	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	if coordinator.state.kind != activityRunning ||
		coordinator.state.requestContext != previous ||
		previous.Err() != nil {
		return false, contextFailure(previous)
	}
	if coordinator.state.cancel != nil {
		coordinator.state.cancel(nil)
	}
	driverContext, cancelActivity := context.WithCancelCause(
		context.Background(),
	)
	initiated, err := agent.WithInitiator(driverContext, coordinator.subject)
	if err != nil {
		cancelActivity(nil)
		return false, err
	}
	coordinator.state.requestContext = initiated
	coordinator.state.cancel = cancelActivity
	coordinator.state.cancelCause = nil
	coordinator.state.wakeRequested = false
	coordinator.state.step = 0
	return true, nil
}

func (coordinator *activityCoordinator) durableCancelCause() session.TurnCancelCause {
	coordinator.mutex.Lock()
	cause := coordinator.state.cancelCause
	coordinator.mutex.Unlock()
	switch selected := cause.(type) {
	case agent.ParentCancel:
		return session.ParentCancelCause{}
	case agent.DisposedCancel:
		return session.DisposedCancelCause{}
	case agent.HookCancel:
		return session.HookCancelCause{
			Reason: selected.Reason,
		}
	case nil, agent.UserCancel:
		return session.UserCancelCause{}
	default:
		return session.HookCancelCause{
			Reason: selected.CancelKind(),
		}
	}
}

func contextFailure(requestContext context.Context) error {
	if requestContext == nil {
		return errors.New("agentloop: operation Context is nil")
	}
	if requestContext.Err() == nil {
		return nil
	}
	if cause := context.Cause(requestContext); cause != nil {
		return cause
	}
	return requestContext.Err()
}
