package agentloop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/tools"
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

// agentDriver owns all mutable execution state for one ReactLoopAgent. The
// Agent object remains the public capability and scoped Plugin identity.
type agentDriver struct {
	subject     *ReactLoopAgent
	pending     *agent.Inbox
	projection  *runtimeContextProjection
	sessions    session.LiveStore
	models      llm.LlmRuntime
	toolRuntime tools.ToolRuntime
	prompts     systemprompt.Assembler

	mutex               sync.Mutex
	activity            activityState
	activityDone        chan struct{}
	disposed            bool
	requestHeaderLogged bool
}

func newAgentDriver(
	subject *ReactLoopAgent,
	pending *agent.Inbox,
	projection *runtimeContextProjection,
	lastTurn int64,
) *agentDriver {
	closedActivity := make(chan struct{})
	close(closedActivity)
	return &agentDriver{
		subject:    subject,
		pending:    pending,
		projection: projection,
		activity: activityState{
			kind:     activityIdle,
			lastTurn: lastTurn,
		},
		activityDone: closedActivity,
	}
}

func (driver *agentDriver) activate(
	requestContext context.Context,
	sessions session.LiveStore,
	models llm.LlmRuntime,
	toolRuntime tools.ToolRuntime,
	prompts systemprompt.Assembler,
) error {
	if err := requestContext.Err(); err != nil {
		return err
	}
	driver.sessions = sessions
	driver.models = models
	driver.toolRuntime = toolRuntime
	driver.prompts = prompts
	return requestContext.Err()
}

func (driver *agentDriver) dispose(closeContext context.Context) error {
	driver.beginDispose()
	idleErr := driver.whenIdle(closeContext)
	driver.sessions = nil
	driver.models = nil
	driver.toolRuntime = nil
	driver.prompts = nil
	return idleErr
}

func (driver *agentDriver) status() agent.Status {
	driver.mutex.Lock()
	defer driver.mutex.Unlock()
	if driver.activity.kind == activityRunning {
		return agent.StatusRunning
	}
	return agent.StatusIdle
}

func (driver *agentDriver) send(
	input llm.UserMessage,
	target agent.InboxTarget,
	wakeup bool,
) error {
	driver.mutex.Lock()
	if driver.disposed {
		driver.mutex.Unlock()
		return fmt.Errorf(
			"agentloop: Agent %q is disposed",
			driver.subject.identifier,
		)
	}
	wakingAfterAbort := wakeup && driver.activity.kind != activityIdle &&
		driver.activity.requestContext != nil &&
		driver.activity.requestContext.Err() != nil
	driver.mutex.Unlock()
	resolvedTarget := target
	if wakingAfterAbort {
		resolvedTarget = agent.NextTurn
	}
	if err := driver.pending.Append(resolvedTarget, input); err != nil {
		return err
	}
	if wakeup {
		return driver.wake(wakingAfterAbort)
	}
	return nil
}

func (driver *agentDriver) cancel(
	cause agent.CancelCause,
	options agent.CancelOptions,
) {
	if cause == nil {
		cause = agent.UserCancel{}
	}
	if !options.KeepInbox {
		if err := driver.pending.Clear(); err != nil {
			driver.subject.owner.report(fmt.Errorf(
				"agentloop: Agent %q clear Inbox during cancel: %w",
				driver.subject.identifier,
				err,
			))
		}
	}
	driver.mutex.Lock()
	if driver.activity.kind != activityIdle {
		if !options.KeepInbox {
			driver.activity.wakeRequested = false
		}
		if driver.activity.cancelCause == nil {
			driver.activity.cancelCause = cause
		}
		if driver.activity.cancel != nil {
			driver.activity.cancel(agentCancellation{
				cause: driver.activity.cancelCause,
			})
		}
	}
	driver.mutex.Unlock()
}

func (driver *agentDriver) beginDispose() {
	driver.mutex.Lock()
	if driver.disposed {
		driver.mutex.Unlock()
		return
	}
	driver.disposed = true
	if driver.activity.kind != activityIdle {
		driver.activity.wakeRequested = false
		if driver.activity.cancelCause == nil {
			driver.activity.cancelCause = agent.DisposedCancel{}
		}
		if driver.activity.cancel != nil {
			driver.activity.cancel(agentCancellation{
				cause: driver.activity.cancelCause,
			})
		}
	}
	driver.mutex.Unlock()
	if err := driver.pending.Clear(); err != nil {
		driver.subject.owner.report(fmt.Errorf(
			"agentloop: Agent %q clear Inbox during disposal: %w",
			driver.subject.identifier,
			err,
		))
	}
}

func (driver *agentDriver) whenIdle(requestContext context.Context) error {
	if requestContext == nil {
		return errors.New("agentloop: WhenIdle Context is nil")
	}
	for {
		driver.mutex.Lock()
		done := driver.activityDone
		idle := driver.activity.kind == activityIdle &&
			!driver.activity.wakeRequested
		driver.mutex.Unlock()
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

func (driver *agentDriver) runMaintenance(
	requestContext context.Context,
	task agent.MaintenanceTask,
) (taskErr error) {
	if requestContext == nil || task == nil {
		return errors.New(
			"agentloop: maintenance Context and task are required",
		)
	}
	operationContext, cancelActivity := context.WithCancelCause(requestContext)
	done := make(chan struct{})
	driver.mutex.Lock()
	if driver.disposed {
		driver.mutex.Unlock()
		cancelActivity(nil)
		return fmt.Errorf(
			"agentloop: Agent %q is disposed",
			driver.subject.identifier,
		)
	}
	if driver.activity.kind != activityIdle {
		driver.mutex.Unlock()
		cancelActivity(nil)
		return fmt.Errorf(
			"agentloop: Agent %q already has active work",
			driver.subject.identifier,
		)
	}
	lastTurn := driver.activity.lastTurn
	driver.activity = activityState{
		kind:           activityMaintenance,
		lastTurn:       lastTurn,
		requestContext: operationContext,
		cancel:         cancelActivity,
	}
	driver.activityDone = done
	driver.mutex.Unlock()
	defer func() {
		cancelActivity(nil)
		driver.finishActivity(done, lastTurn, false)
	}()
	return task.Run(operationContext)
}

func (driver *agentDriver) wake(wakingAfterAbort bool) error {
	driver.mutex.Lock()
	if driver.disposed {
		driver.mutex.Unlock()
		return fmt.Errorf(
			"agentloop: Agent %q is disposed",
			driver.subject.identifier,
		)
	}
	if driver.activity.kind != activityIdle {
		disposedCause := driver.activity.cancelCause != nil &&
			driver.activity.cancelCause.CancelKind() ==
				(agent.DisposedCancel{}).CancelKind()
		if !disposedCause &&
			(driver.activity.kind == activityMaintenance || wakingAfterAbort) {
			driver.activity.wakeRequested = true
		}
		driver.mutex.Unlock()
		return nil
	}
	driverContext, cancelActivity := context.WithCancelCause(
		context.Background(),
	)
	initiated, err := agent.WithInitiator(driverContext, driver.subject)
	if err != nil {
		driver.mutex.Unlock()
		cancelActivity(nil)
		return err
	}
	done := make(chan struct{})
	lastTurn := driver.activity.lastTurn
	driver.activity = activityState{
		kind:           activityRunning,
		lastTurn:       lastTurn,
		turn:           lastTurn,
		requestContext: initiated,
		cancel:         cancelActivity,
	}
	driver.activityDone = done
	driver.mutex.Unlock()
	driver.emitStatus(agent.StatusRunning)
	go driver.drive(done)
	return nil
}

func (driver *agentDriver) drive(done chan struct{}) {
	for {
		driver.mutex.Lock()
		requestContext := driver.activity.requestContext
		driver.mutex.Unlock()
		continueDriving, _ := driver.runTurn(requestContext)
		if !continueDriving {
			break
		}
	}
	driver.mutex.Lock()
	turn := driver.activity.turn
	if driver.activity.cancel != nil {
		driver.activity.cancel(nil)
	}
	driver.mutex.Unlock()
	driver.finishActivity(done, turn, true)
}

// finishActivity preserves a latched wake until the successor activity has
// reserved its completion channel. This keeps whenIdle from observing a
// transient idle state between logically connected activities.
func (driver *agentDriver) finishActivity(
	done chan struct{},
	lastTurn int64,
	publishIdle bool,
) {
	driver.mutex.Lock()
	wakeRequested := driver.activity.wakeRequested
	driver.activity = activityState{
		kind:          activityIdle,
		lastTurn:      lastTurn,
		wakeRequested: wakeRequested,
	}
	driver.mutex.Unlock()
	if publishIdle {
		driver.emitStatus(agent.StatusIdle)
	}
	if wakeRequested && driver.pending.HasPending() {
		if err := driver.wake(false); err != nil {
			driver.subject.owner.report(err)
		}
	}
	driver.mutex.Lock()
	if driver.activity.kind == activityIdle {
		driver.activity.wakeRequested = false
	}
	driver.mutex.Unlock()
	close(done)
}

func (driver *agentDriver) renewTurnContext(previous context.Context) (bool, error) {
	driver.mutex.Lock()
	defer driver.mutex.Unlock()
	if driver.activity.kind != activityRunning ||
		driver.activity.requestContext != previous || previous.Err() != nil {
		return false, contextFailure(previous)
	}
	if driver.activity.cancel != nil {
		driver.activity.cancel(nil)
	}
	driverContext, cancelActivity := context.WithCancelCause(
		context.Background(),
	)
	initiated, err := agent.WithInitiator(driverContext, driver.subject)
	if err != nil {
		cancelActivity(nil)
		return false, err
	}
	driver.activity.requestContext = initiated
	driver.activity.cancel = cancelActivity
	driver.activity.cancelCause = nil
	driver.activity.wakeRequested = false
	driver.activity.step = 0
	return true, nil
}

func (driver *agentDriver) durableCancelCause() session.TurnCancelCause {
	driver.mutex.Lock()
	cause := driver.activity.cancelCause
	driver.mutex.Unlock()
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
		return session.LegacyCancelCause{}
	}
}

func (driver *agentDriver) acceptSessionEvent(committed session.Event) {
	if driver.projection != nil {
		driver.projection.accept(committed)
	}
}

func restoreLastTurn(conversation *session.Session) (int64, error) {
	entries := conversation.Events()
	for index := len(entries) - 1; index >= 0; index-- {
		if entries[index].Type != session.TurnStartEventName {
			continue
		}
		var started session.TurnStart
		if err := json.Unmarshal(entries[index].Data, &started); err != nil ||
			started.Turn <= 0 || started.Turn > maxSafeInteger {
			return 0, fmt.Errorf(
				"agentloop: invalid persisted turn/start at seq %d",
				entries[index].Seq,
			)
		}
		return started.Turn, nil
	}
	return 0, nil
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
