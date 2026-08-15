package agentloop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/systemprompt"
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

// ReactLoopAgent is the concrete default Agent driver. Session is its durable
// source of truth; this object owns only live coordination and projections.
type ReactLoopAgent struct {
	owner        *loopService
	identifier   session.SessionID
	options      agent.Options
	conversation *session.Session
	pending      *agent.Inbox
	agentScope   *plugin.Scope
	projection   *runtimeContextProjection

	mu                  sync.Mutex
	activity            activityState
	activityDone        chan struct{}
	disposed            bool
	requestHeaderLogged bool
}

func newReactLoopAgent(
	owner *loopService,
	conversation *session.Session,
	loopOptions agent.Options,
	agentScope *plugin.Scope,
) (*ReactLoopAgent, error) {
	if owner == nil || conversation == nil || agentScope == nil {
		return nil, errors.New("agentloop: Agent owner, Session, and Scope are required")
	}
	lastTurn, err := restoreLastTurn(conversation)
	if err != nil {
		return nil, err
	}
	closedActivity := make(chan struct{})
	close(closedActivity)
	subject := &ReactLoopAgent{
		owner: owner, identifier: conversation.ID(), options: cloneAgentOptions(loopOptions),
		conversation: conversation, agentScope: agentScope,
		activity: activityState{kind: activityIdle, lastTurn: lastTurn}, activityDone: closedActivity,
	}
	pending, err := agent.NewInbox(conversation, inboxEventBridge{subject: subject})
	if err != nil {
		return nil, err
	}
	subject.pending = pending
	projection, err := newRuntimeContextProjection(agentScope, conversation)
	if err != nil {
		return nil, err
	}
	subject.projection = projection
	return subject, nil
}

func (subject *ReactLoopAgent) ID() session.SessionID { return subject.identifier }

func (subject *ReactLoopAgent) OptionsValue() agent.Options {
	return cloneAgentOptions(subject.options)
}

func (subject *ReactLoopAgent) SessionValue() *session.Session { return subject.conversation }

func (subject *ReactLoopAgent) InboxValue() *agent.Inbox { return subject.pending }

func (subject *ReactLoopAgent) ScopeValue() *plugin.Scope { return subject.agentScope }

func (subject *ReactLoopAgent) StatusValue() agent.Status {
	subject.mu.Lock()
	defer subject.mu.Unlock()
	if subject.activity.kind == activityRunning {
		return agent.StatusRunning
	}
	return agent.StatusIdle
}

func (subject *ReactLoopAgent) Send(input llm.UserMessage, target agent.InboxTarget, wakeup bool) error {
	subject.mu.Lock()
	if subject.disposed {
		subject.mu.Unlock()
		return fmt.Errorf("agentloop: Agent %q is disposed", subject.identifier)
	}
	wakingAfterAbort := wakeup && subject.activity.kind != activityIdle &&
		subject.activity.requestContext != nil && subject.activity.requestContext.Err() != nil
	subject.mu.Unlock()
	resolvedTarget := target
	if wakingAfterAbort {
		resolvedTarget = agent.NextTurn
	}
	if err := subject.pending.Append(resolvedTarget, input); err != nil {
		return err
	}
	if wakeup {
		return subject.wakeDriver(wakingAfterAbort)
	}
	return nil
}

func (subject *ReactLoopAgent) Followup(input llm.UserMessage) error {
	return subject.Send(input, agent.NextTurn, true)
}

func (subject *ReactLoopAgent) Steer(input llm.UserMessage) error {
	return subject.Send(input, agent.NextStep, true)
}

func (subject *ReactLoopAgent) Inject(input llm.UserMessage) error {
	return subject.Send(input, agent.NextStep, false)
}

func (subject *ReactLoopAgent) Cancel(cause agent.CancelCause, options agent.CancelOptions) {
	if cause == nil {
		cause = agent.UserCancel{}
	}
	if !options.KeepInbox {
		if err := subject.pending.Clear(); err != nil {
			subject.owner.report(fmt.Errorf("agentloop: Agent %q clear Inbox during cancel: %w", subject.identifier, err))
		}
	}
	subject.mu.Lock()
	if subject.activity.kind != activityIdle {
		if !options.KeepInbox {
			subject.activity.wakeRequested = false
		}
		if subject.activity.cancelCause == nil {
			subject.activity.cancelCause = cause
		}
		if subject.activity.cancel != nil {
			subject.activity.cancel(agentCancellation{cause: subject.activity.cancelCause})
		}
	}
	subject.mu.Unlock()
}

func (subject *ReactLoopAgent) beginDispose() {
	subject.mu.Lock()
	if subject.disposed {
		subject.mu.Unlock()
		return
	}
	subject.disposed = true
	if subject.activity.kind != activityIdle {
		subject.activity.wakeRequested = false
		if subject.activity.cancelCause == nil {
			subject.activity.cancelCause = agent.DisposedCancel{}
		}
		if subject.activity.cancel != nil {
			subject.activity.cancel(agentCancellation{cause: subject.activity.cancelCause})
		}
	}
	subject.mu.Unlock()
	if err := subject.pending.Clear(); err != nil {
		subject.owner.report(fmt.Errorf("agentloop: Agent %q clear Inbox during disposal: %w", subject.identifier, err))
	}
}

func (subject *ReactLoopAgent) WhenIdle(requestContext context.Context) error {
	if requestContext == nil {
		return errors.New("agentloop: WhenIdle Context is nil")
	}
	for {
		subject.mu.Lock()
		done := subject.activityDone
		idle := subject.activity.kind == activityIdle && !subject.activity.wakeRequested
		subject.mu.Unlock()
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

func (subject *ReactLoopAgent) RunMaintenance(requestContext context.Context, task agent.MaintenanceTask) (taskErr error) {
	if requestContext == nil || task == nil {
		return errors.New("agentloop: maintenance Context and task are required")
	}
	operationContext, cancelActivity := context.WithCancelCause(requestContext)
	done := make(chan struct{})
	subject.mu.Lock()
	if subject.disposed {
		subject.mu.Unlock()
		cancelActivity(nil)
		return fmt.Errorf("agentloop: Agent %q is disposed", subject.identifier)
	}
	if subject.activity.kind != activityIdle {
		subject.mu.Unlock()
		cancelActivity(nil)
		return fmt.Errorf("agentloop: Agent %q already has active work", subject.identifier)
	}
	lastTurn := subject.activity.lastTurn
	subject.activity = activityState{
		kind: activityMaintenance, lastTurn: lastTurn,
		requestContext: operationContext, cancel: cancelActivity,
	}
	subject.activityDone = done
	subject.mu.Unlock()
	defer func() {
		cancelActivity(nil)
		subject.finishActivity(done, lastTurn, false)
	}()
	return task.Run(operationContext)
}

func (subject *ReactLoopAgent) wakeDriver(wakingAfterAbort bool) error {
	subject.mu.Lock()
	if subject.disposed {
		subject.mu.Unlock()
		return fmt.Errorf("agentloop: Agent %q is disposed", subject.identifier)
	}
	if subject.activity.kind != activityIdle {
		disposedCause := subject.activity.cancelCause != nil &&
			subject.activity.cancelCause.CancelKind() == (agent.DisposedCancel{}).CancelKind()
		if !disposedCause && (subject.activity.kind == activityMaintenance || wakingAfterAbort) {
			subject.activity.wakeRequested = true
		}
		subject.mu.Unlock()
		return nil
	}
	driverContext, cancelActivity := context.WithCancelCause(context.Background())
	initiated, err := agent.WithInitiator(driverContext, subject)
	if err != nil {
		subject.mu.Unlock()
		cancelActivity(nil)
		return err
	}
	done := make(chan struct{})
	lastTurn := subject.activity.lastTurn
	subject.activity = activityState{
		kind: activityRunning, lastTurn: lastTurn, turn: lastTurn,
		requestContext: initiated, cancel: cancelActivity,
	}
	subject.activityDone = done
	subject.mu.Unlock()
	subject.emitStatus(agent.StatusRunning)
	go subject.drive(done)
	return nil
}

func (subject *ReactLoopAgent) drive(done chan struct{}) {
	for {
		subject.mu.Lock()
		requestContext := subject.activity.requestContext
		subject.mu.Unlock()
		continueDriving, _ := subject.runTurn(requestContext)
		if !continueDriving {
			break
		}
	}
	subject.mu.Lock()
	turn := subject.activity.turn
	if subject.activity.cancel != nil {
		subject.activity.cancel(nil)
	}
	subject.mu.Unlock()
	subject.finishActivity(done, turn, true)
}

// finishActivity preserves a latched wake until the successor activity has
// reserved its own completion channel. This keeps WhenIdle from observing the
// transient idle state between two logically connected activities.
func (subject *ReactLoopAgent) finishActivity(done chan struct{}, lastTurn int64, publishIdle bool) {
	subject.mu.Lock()
	wakeRequested := subject.activity.wakeRequested
	subject.activity = activityState{
		kind: activityIdle, lastTurn: lastTurn, wakeRequested: wakeRequested,
	}
	subject.mu.Unlock()
	if publishIdle {
		subject.emitStatus(agent.StatusIdle)
	}
	if wakeRequested && subject.pending.HasPending() {
		if err := subject.wakeDriver(false); err != nil {
			subject.owner.report(err)
		}
	}
	subject.mu.Lock()
	if subject.activity.kind == activityIdle {
		subject.activity.wakeRequested = false
	}
	subject.mu.Unlock()
	close(done)
}

type preparedStep struct {
	rejected bool
	messages []llm.UserMessage
	assembly systemprompt.PromptAssembly
}

func (subject *ReactLoopAgent) prepareStep(
	requestContext context.Context,
	target agent.InboxTarget,
	turn int64,
	step int64,
) (preparedStep, error) {
	if err := contextFailure(requestContext); err != nil {
		return preparedStep{}, err
	}
	claimedMessages, err := subject.pending.Claim(target, turn)
	if err != nil {
		return preparedStep{}, err
	}
	assembled, err := subject.owner.prompts.Assemble(requestContext, systemprompt.AssembleContext{
		Scope: subject.agentScope.Target(), Session: subject.conversation,
	})
	if err != nil {
		return preparedStep{}, err
	}
	sections, err := systemprompt.RenderContextSections(assembled)
	if err != nil {
		return preparedStep{}, err
	}
	projected, present, err := subject.projection.project(systemprompt.JoinContextSections(sections), sections)
	if err != nil {
		return preparedStep{}, err
	}
	candidates := claimedMessages
	if present {
		candidates = append(candidates, projected)
	}
	decision, err := agent.ResolvePreStep(requestContext, subject.owner.sourceScope, agent.PreStepNotice{
		Subject: subject, Messages: candidates, Turn: turn, Step: step,
	}, func(context.Context) (agent.PreStepDecision, error) {
		return agent.PreStepDecision{Kind: agent.PreStepEnter, Messages: candidates}, nil
	})
	if err != nil {
		return preparedStep{}, err
	}
	if err := contextFailure(requestContext); err != nil {
		return preparedStep{}, err
	}
	switch decision.Kind {
	case agent.PreStepReject:
		return preparedStep{rejected: true}, nil
	case agent.PreStepEnter:
		detached, err := cloneUserMessages(decision.Messages)
		if err != nil {
			return preparedStep{}, err
		}
		return preparedStep{messages: detached, assembly: assembled}, nil
	default:
		return preparedStep{}, fmt.Errorf("agentloop: unsupported pre-step decision %q", decision.Kind)
	}
}

func (subject *ReactLoopAgent) runTurn(requestContext context.Context) (bool, error) {
	if err := contextFailure(requestContext); err != nil {
		return false, err
	}
	subject.mu.Lock()
	turn := subject.activity.turn + 1
	subject.mu.Unlock()
	if turn <= 0 || turn > maxSafeInteger {
		problem := fmt.Errorf("agentloop: Agent %q turn exceeds the safe integer range", subject.identifier)
		subject.reportError(requestContext, problem)
		return false, problem
	}
	if _, err := session.Append(subject.conversation, session.TurnStarted, session.TurnStart{Turn: turn}); err != nil {
		subject.reportError(requestContext, err)
		return false, err
	}
	subject.mu.Lock()
	subject.activity.turn = turn
	subject.mu.Unlock()

	var ending session.TurnEndReason
	var operationErr error
	target := agent.NextTurn
	for {
		if err := contextFailure(requestContext); err != nil {
			operationErr = err
			ending = session.TurnAborted{Reason: subject.durableCancelCause()}
			break
		}
		subject.mu.Lock()
		step := subject.activity.step + 1
		priorStep := subject.activity.step
		subject.mu.Unlock()
		prepared, err := subject.prepareStep(requestContext, target, turn, step)
		if err != nil {
			operationErr = err
			if requestContext.Err() != nil {
				ending = session.TurnAborted{Reason: subject.durableCancelCause()}
			} else {
				ending = session.TurnError{Error: failureFromError(err)}
				subject.reportError(requestContext, err)
			}
			break
		}
		if prepared.rejected {
			ending = session.TurnBlocked{}
			break
		}
		if ending != nil && len(prepared.messages) == 0 {
			break
		}
		if priorStep == 0 && len(prepared.messages) == 0 {
			ending = session.TurnCompleted{}
			break
		}
		if _, err := session.Append(subject.conversation, session.StepStarted, session.StepPosition{Turn: turn, Step: step}); err != nil {
			operationErr = err
			ending = session.TurnError{Error: failureFromError(err)}
			subject.reportError(requestContext, err)
			break
		}
		subject.mu.Lock()
		subject.activity.step = step
		subject.mu.Unlock()
		stepErr := subject.appendStepMessages(prepared.messages)
		var stepEnding session.TurnEndReason
		if stepErr == nil {
			stepEnding, stepErr = subject.executeStep(requestContext, turn, step, prepared)
		}
		_, endErr := session.Append(subject.conversation, session.StepEnded, session.StepPosition{Turn: turn, Step: step})
		if stepErr != nil || endErr != nil {
			operationErr = errors.Join(stepErr, endErr)
			if requestContext.Err() != nil {
				ending = session.TurnAborted{Reason: subject.durableCancelCause()}
			} else {
				ending = session.TurnError{Error: failureFromError(operationErr)}
				subject.reportError(requestContext, operationErr)
			}
			break
		}
		if stepEnding != nil {
			if ending == nil || ending.TurnEndKind() != (session.TurnMaxTokens{}).TurnEndKind() {
				ending = stepEnding
			}
		}
		if ending != nil && len(subject.pending.NextStep()) == 0 {
			if err := agent.DispatchTurnStopping(requestContext, subject.owner.sourceScope, subject, turn); err != nil {
				operationErr = err
				ending = session.TurnError{Error: failureFromError(err)}
				subject.reportError(requestContext, err)
				break
			}
			if err := contextFailure(requestContext); err != nil {
				operationErr = err
				ending = session.TurnAborted{Reason: subject.durableCancelCause()}
				break
			}
		}
		if ending != nil && len(subject.pending.NextStep()) == 0 {
			break
		}
		target = agent.NextStep
	}
	if ending == nil {
		ending = session.TurnError{Error: llm.LlmFailure{Message: "turn ended without a reason", Code: "UNKNOWN"}}
	}
	if _, err := session.Append(subject.conversation, session.TurnEnded, session.TurnEnd{Turn: turn, Reason: ending}); err != nil {
		operationErr = errors.Join(operationErr, err)
		subject.reportError(requestContext, err)
	}
	if operationErr != nil {
		return false, operationErr
	}
	if !subject.pending.HasPending() {
		return false, nil
	}
	return subject.renewTurnContext(requestContext)
}

func (subject *ReactLoopAgent) appendStepMessages(messages []llm.UserMessage) error {
	for _, message := range messages {
		if _, err := session.AppendSurface(subject.conversation, session.UserMessageAdded, message, session.SurfaceIntent{
			Operation: session.SurfaceAppend(),
		}); err != nil {
			return err
		}
	}
	return nil
}

func (subject *ReactLoopAgent) renewTurnContext(previous context.Context) (bool, error) {
	subject.mu.Lock()
	defer subject.mu.Unlock()
	if subject.activity.kind != activityRunning || subject.activity.requestContext != previous || previous.Err() != nil {
		return false, contextFailure(previous)
	}
	if subject.activity.cancel != nil {
		subject.activity.cancel(nil)
	}
	driverContext, cancelActivity := context.WithCancelCause(context.Background())
	initiated, err := agent.WithInitiator(driverContext, subject)
	if err != nil {
		cancelActivity(nil)
		return false, err
	}
	subject.activity.requestContext = initiated
	subject.activity.cancel = cancelActivity
	subject.activity.cancelCause = nil
	subject.activity.wakeRequested = false
	subject.activity.step = 0
	return true, nil
}

func (subject *ReactLoopAgent) durableCancelCause() session.TurnCancelCause {
	subject.mu.Lock()
	cause := subject.activity.cancelCause
	subject.mu.Unlock()
	switch selected := cause.(type) {
	case agent.ParentCancel:
		return session.ParentCancelCause{}
	case agent.DisposedCancel:
		return session.DisposedCancelCause{}
	case agent.HookCancel:
		return session.HookCancelCause{Reason: selected.Reason}
	case nil, agent.UserCancel:
		return session.UserCancelCause{}
	default:
		return session.LegacyCancelCause{}
	}
}

func (subject *ReactLoopAgent) emitStatus(destination agent.Status) {
	if err := agent.EmitStatus(context.Background(), subject.owner.sourceScope, subject, destination); err != nil {
		subject.owner.report(fmt.Errorf("agentloop: Agent %q status observer: %w", subject.identifier, err))
	}
}

func (subject *ReactLoopAgent) reportError(requestContext context.Context, problem error) {
	subject.mu.Lock()
	turn := subject.activity.turn
	step := subject.activity.step
	subject.mu.Unlock()
	if requestContext == nil {
		requestContext = context.Background()
	}
	if err := agent.EmitError(requestContext, subject.owner.sourceScope, agent.ErrorNotice{
		Subject: subject, Turn: turn, Step: step, Err: problem,
	}); err != nil {
		subject.owner.report(fmt.Errorf("agentloop: Agent %q error observer: %w", subject.identifier, err))
	}
}

type inboxEventBridge struct {
	subject *ReactLoopAgent
}

func (bridge inboxEventBridge) Inserted(input llm.UserMessage) {
	if err := agent.EmitInboxInserted(context.Background(), bridge.subject.owner.sourceScope, bridge.subject, input); err != nil {
		bridge.subject.owner.report(err)
	}
}

func (bridge inboxEventBridge) Discarded(input llm.UserMessage) {
	if err := agent.EmitInboxDiscarded(context.Background(), bridge.subject.owner.sourceScope, bridge.subject, input); err != nil {
		bridge.subject.owner.report(err)
	}
}

func (bridge inboxEventBridge) Claimed(input llm.UserMessage, turn int64) {
	if err := agent.EmitInboxClaimed(context.Background(), bridge.subject.owner.sourceScope, bridge.subject, input, turn); err != nil {
		bridge.subject.owner.report(err)
	}
}

type agentCancellation struct {
	cause agent.CancelCause
}

func (problem agentCancellation) Error() string {
	if problem.cause == nil {
		return "agent canceled"
	}
	return "agent canceled: " + problem.cause.CancelKind()
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

func restoreLastTurn(conversation *session.Session) (int64, error) {
	entries := conversation.Events()
	for index := len(entries) - 1; index >= 0; index-- {
		if entries[index].Type != session.TurnStartEventName {
			continue
		}
		var started session.TurnStart
		if err := json.Unmarshal(entries[index].Data, &started); err != nil || started.Turn <= 0 || started.Turn > maxSafeInteger {
			return 0, fmt.Errorf("agentloop: invalid persisted turn/start at seq %d", entries[index].Seq)
		}
		return started.Turn, nil
	}
	return 0, nil
}

func failureFromError(problem error) llm.LlmFailure {
	var llmProblem *llm.LlmError
	if errors.As(problem, &llmProblem) {
		return llmProblem.Failure()
	}
	if problem == nil {
		return llm.LlmFailure{Message: "unknown Agent Loop failure", Code: "UNKNOWN"}
	}
	return llm.LlmFailure{Message: problem.Error(), Code: "UNKNOWN"}
}

func cloneAgentOptions(source agent.Options) agent.Options {
	detached := source
	detached.MaxTokens = cloneInt(source.MaxTokens)
	return detached
}

func cloneUserMessages(entries []llm.UserMessage) ([]llm.UserMessage, error) {
	if entries == nil {
		return nil, nil
	}
	detached := make([]llm.UserMessage, len(entries))
	for index, entry := range entries {
		copyValue, err := llm.CloneUserMessage(entry)
		if err != nil {
			return nil, fmt.Errorf("agentloop: clone user message %d: %w", index, err)
		}
		detached[index] = copyValue
	}
	return detached, nil
}
