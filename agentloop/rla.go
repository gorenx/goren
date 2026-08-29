package agentloop

import (
	"context"
	"errors"
	"fmt"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentloop/internal/execution"
	"github.com/gorenx/goren/agentloop/internal/lifecycle"
	"github.com/gorenx/goren/agentloop/internal/visiblecontext"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/tools"
)

// ReactLoopAgent is the concrete Agent business capability and the only
// effectful orchestrator of its execution state machines.
type ReactLoopAgent struct {
	identifier           session.SessionID
	options              agent.Options
	conversation         session.Context
	pending              *agent.Inbox
	lifecycle            *lifecycle.AgentLifecycle
	execution            *execution.AgentExecution
	visibleContext       *visiblecontext.VisibleContext
	maxParallelToolCalls int
	scopeRuntime         agent.AgentScopeRuntime
	reportObserverError  func(error)

	sessions            session.LiveStore
	models              llm.LlmRuntime
	toolRuntime         tools.ToolRuntime
	prompts             systemprompt.Assembler
	lastTurn            int64
	requestHeaderLogged bool
}

func newReactLoopAgent(
	conversation session.Context,
	agentOptions agent.Options,
	maxParallelToolCalls int,
	reportObserverError func(error),
	scopeRuntime agent.AgentScopeRuntime,
) (*ReactLoopAgent, error) {
	if conversation == nil {
		return nil, errors.New("agentloop: Agent Session is required")
	}
	if maxParallelToolCalls < 1 {
		return nil, errors.New(
			"agentloop: Agent Tool concurrency must be positive",
		)
	}
	if scopeRuntime == nil {
		return nil, errors.New("agentloop: Agent Scope Runtime is required")
	}
	lastTurn, err := restoreLastTurn(conversation)
	if err != nil {
		return nil, err
	}
	visible, err := visiblecontext.New(conversation)
	if err != nil {
		return nil, err
	}
	if reportObserverError == nil {
		reportObserverError = func(error) {}
	}
	subject := &ReactLoopAgent{
		identifier:           conversation.ID(),
		options:              cloneAgentOptions(agentOptions),
		conversation:         conversation,
		lifecycle:            lifecycle.New(),
		execution:            execution.New(),
		visibleContext:       visible,
		maxParallelToolCalls: maxParallelToolCalls,
		scopeRuntime:         scopeRuntime,
		reportObserverError:  reportObserverError,
		lastTurn:             lastTurn,
	}
	pending, err := agent.NewInbox(
		conversation,
		inboxNotifications{
			subject:             subject,
			runtime:             scopeRuntime,
			identifier:          string(subject.identifier),
			reportObserverError: subject.reportObserverError,
		},
	)
	if err != nil {
		return nil, err
	}
	subject.pending = pending
	return subject, nil
}

func (subject *ReactLoopAgent) activate(
	requestContext context.Context,
	sessions session.LiveStore,
	models llm.LlmRuntime,
	toolRuntime tools.ToolRuntime,
	prompts systemprompt.Assembler,
) error {
	if requestContext == nil {
		return errors.New("agentloop: Agent activation Context is nil")
	}
	if err := requestContext.Err(); err != nil {
		return err
	}
	if sessions == nil || models == nil || toolRuntime == nil || prompts == nil {
		return errors.New("agentloop: Agent execution dependencies are incomplete")
	}
	subject.sessions = sessions
	subject.models = models
	subject.toolRuntime = toolRuntime
	subject.prompts = prompts
	return requestContext.Err()
}

func (subject *ReactLoopAgent) enterServing() error {
	if subject == nil {
		return errors.New("agentloop: Agent is nil")
	}
	if err := subject.lifecycle.EnterServing(); err != nil {
		return fmt.Errorf(
			"agentloop: Agent %q begin serving: %w",
			subject.identifier,
			err,
		)
	}
	return nil
}

func (subject *ReactLoopAgent) shutdown(closeContext context.Context) error {
	if subject == nil {
		return nil
	}
	if closeContext == nil {
		closeContext = context.Background()
	}
	invocationsDrained := subject.lifecycle.EnterClosing()
	select {
	case <-invocationsDrained:
	case <-closeContext.Done():
		return context.Cause(closeContext)
	}
	subject.execution.DiscardWakeRequest()
	subject.cancelExecution(agent.DisposedCancel{}, false)
	if err := subject.pending.Clear(); err != nil {
		subject.observeError(fmt.Errorf(
			"agentloop: Agent %q clear Inbox during disposal: %w",
			subject.identifier,
			err,
		))
	}
	if err := subject.WhenIdle(closeContext); err != nil {
		return err
	}
	subject.sessions = nil
	subject.models = nil
	subject.toolRuntime = nil
	subject.prompts = nil
	return subject.lifecycle.EnterClosed()
}

func (subject *ReactLoopAgent) ID() session.SessionID { return subject.identifier }

func (subject *ReactLoopAgent) ScopeRuntimeValue() agent.AgentScopeRuntime {
	return subject.scopeRuntime
}

func (subject *ReactLoopAgent) OptionsValue() agent.Options {
	return cloneAgentOptions(subject.options)
}

func (subject *ReactLoopAgent) SessionValue() session.Context {
	return subject.conversation
}

func (subject *ReactLoopAgent) InboxValue() *agent.Inbox { return subject.pending }

func (subject *ReactLoopAgent) StatusValue() agent.Status {
	if subject.execution.Running() {
		return agent.StatusRunning
	}
	return agent.StatusIdle
}

func (subject *ReactLoopAgent) Send(
	input agentmessage.UserMessage,
	target agent.InboxTarget,
	wakeup bool,
) error {
	admitted, err := subject.lifecycle.AdmitInvocation()
	if err != nil {
		return fmt.Errorf(
			"agentloop: Agent %q is not live: %w",
			subject.identifier,
			err,
		)
	}
	defer subject.finishInvocation(admitted)
	resolvedTarget := target
	snapshot := subject.execution.Snapshot()
	operationContext := subject.execution.OperationContext(snapshot.Generation)
	if wakeup && operationContext != nil && operationContext.Err() != nil &&
		snapshot.Cancellation.Kind != "disposed" {
		resolvedTarget = agent.NextTurn
	}
	if err = subject.pending.Append(resolvedTarget, input); err != nil {
		return err
	}
	if wakeup {
		return subject.wake()
	}
	return nil
}

func (subject *ReactLoopAgent) Followup(input agentmessage.UserMessage) error {
	return subject.Send(input, agent.NextTurn, true)
}

func (subject *ReactLoopAgent) Steer(input agentmessage.UserMessage) error {
	return subject.Send(input, agent.NextStep, true)
}

func (subject *ReactLoopAgent) Inject(input agentmessage.UserMessage) error {
	return subject.Send(input, agent.NextStep, false)
}

func (subject *ReactLoopAgent) Cancel(
	cause agent.CancelCause,
	options agent.CancelOptions,
) {
	admitted, err := subject.lifecycle.AdmitInvocation()
	if err != nil {
		return
	}
	defer subject.finishInvocation(admitted)
	if cause == nil {
		cause = agent.UserCancel{}
	}
	if !options.KeepInbox {
		if err := subject.pending.Clear(); err != nil {
			subject.observeError(fmt.Errorf(
				"agentloop: Agent %q clear Inbox during cancel: %w",
				subject.identifier,
				err,
			))
		}
	}
	subject.cancelExecution(cause, options.KeepInbox)
}

func (subject *ReactLoopAgent) WhenIdle(requestContext context.Context) error {
	if requestContext == nil {
		return errors.New("agentloop: WhenIdle Context is nil")
	}
	for {
		idle, done := subject.execution.IdleWait()
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

func (subject *ReactLoopAgent) RunMaintenance(
	requestContext context.Context,
	operation func(context.Context) error,
) error {
	if requestContext == nil || operation == nil {
		return errors.New(
			"agentloop: maintenance Context and operation are required",
		)
	}
	admitted, err := subject.lifecycle.AdmitInvocation()
	if err != nil {
		return fmt.Errorf(
			"agentloop: Agent %q is not live: %w",
			subject.identifier,
			err,
		)
	}
	defer subject.finishInvocation(admitted)
	operationContext, cancelOperation := context.WithCancelCause(requestContext)
	selected, err := subject.execution.EnterMaintenance(
		operationContext,
		cancelOperation,
	)
	if err != nil {
		cancelOperation(nil)
		return fmt.Errorf(
			"agentloop: Agent %q already has active or queued work: %w",
			subject.identifier,
			err,
		)
	}
	operationErr := operation(operationContext)
	if err = subject.execution.EnterMaintenanceSettling(selected); err != nil {
		return errors.Join(operationErr, err)
	}
	if subject.execution.WakeRequested(selected) && subject.pending.HasPending() &&
		subject.lifecycle.StateValue() == lifecycle.StateServing {
		nextContext, cancelNext, contextErr := subject.newTurnContext()
		if contextErr != nil {
			_ = subject.execution.EnterIdle(selected)
			return errors.Join(operationErr, contextErr)
		}
		nextGeneration, continueErr := subject.execution.EnterTurnAfterMaintenance(
			selected,
			nextContext,
			cancelNext,
		)
		if continueErr != nil {
			cancelNext(nil)
			_ = subject.execution.EnterIdle(selected)
			return errors.Join(operationErr, continueErr)
		}
		subject.publishStatus(agent.StatusRunning)
		go subject.driveTurns(nextGeneration)
		return operationErr
	}
	return errors.Join(operationErr, subject.execution.EnterIdle(selected))
}

func (subject *ReactLoopAgent) wake() error {
	requestContext, cancelOperation, err := subject.newTurnContext()
	if err != nil {
		return err
	}
	entry, err := subject.execution.EnterTurnOrRequestWake(
		requestContext,
		cancelOperation,
	)
	if err != nil {
		cancelOperation(nil)
		return err
	}
	if !entry.Entered {
		cancelOperation(nil)
		return nil
	}
	subject.publishStatus(agent.StatusRunning)
	go subject.driveTurns(entry.Generation)
	return nil
}

func (subject *ReactLoopAgent) driveTurns(selected execution.Generation) {
	for {
		snapshot := subject.execution.Snapshot()
		operationContext := subject.execution.OperationContext(selected)
		if snapshot.Generation != selected || operationContext == nil {
			subject.observeError(fmt.Errorf(
				"agentloop: Agent %q lost Turn execution generation %d",
				subject.identifier,
				selected,
			))
			return
		}
		continueTurns, _ := subject.runTurn(operationContext)
		if err := subject.execution.EnterTurnSettling(selected); err != nil {
			subject.observeError(err)
			return
		}
		shouldContinue := subject.pending.HasPending() &&
			(continueTurns || subject.execution.WakeRequested(selected)) &&
			subject.lifecycle.StateValue() == lifecycle.StateServing
		if shouldContinue {
			nextContext, cancelNext, err := subject.newTurnContext()
			if err == nil {
				selected, err = subject.execution.EnterSuccessorTurn(
					selected,
					nextContext,
					cancelNext,
				)
			}
			if err == nil {
				continue
			}
			if cancelNext != nil {
				cancelNext(nil)
			}
			subject.observeError(err)
		}
		if err := subject.execution.EnterIdle(selected); err != nil {
			subject.observeError(err)
			return
		}
		if subject.lifecycle.StateValue() == lifecycle.StateServing {
			subject.publishStatus(agent.StatusIdle)
		}
		return
	}
}

func (subject *ReactLoopAgent) newTurnContext() (
	context.Context,
	context.CancelCauseFunc,
	error,
) {
	requestContext, cancelOperation := context.WithCancelCause(
		context.Background(),
	)
	initiated, err := agent.WithInitiator(requestContext, subject)
	if err != nil {
		cancelOperation(nil)
		return nil, nil, err
	}
	return initiated, cancelOperation, nil
}

func (subject *ReactLoopAgent) cancelExecution(
	cause agent.CancelCause,
	keepWake bool,
) {
	detail := execution.Cancellation{
		Kind: cause.CancelKind(),
	}
	if selected, ok := cause.(agent.HookCancel); ok {
		detail.Reason = selected.Reason
	}
	subject.execution.RecordCancellation(
		detail,
		agentCancellation{
			cause: cause,
		},
		keepWake,
	)
}

func (subject *ReactLoopAgent) finishInvocation(admitted lifecycle.AgentInvocation) {
	if err := subject.lifecycle.FinishInvocation(admitted); err != nil {
		subject.observeError(err)
	}
}

func (subject *ReactLoopAgent) observeError(problem error) {
	if problem == nil || subject == nil || subject.reportObserverError == nil {
		return
	}
	defer func() {
		_ = recover()
	}()
	subject.reportObserverError(problem)
}

func cloneAgentOptions(source agent.Options) agent.Options {
	detached := source
	detached.MaxTokens = cloneInt(source.MaxTokens)
	return detached
}
