package basic

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/compaction"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
)

// automationController owns the Runtime hooks and cross-hook overflow recovery
// state that invoke Compaction. It does not implement compaction transactions.
type automationController struct {
	engine  compaction.Engine
	catalog *policyCatalog
	report  func(error)

	mutex            sync.Mutex
	overflowSequence map[session.Context]overflowRecovery
	warnedTargets    map[string]struct{}
}

func newAutomationController(
	engine compaction.Engine,
	catalog *policyCatalog,
	reporter func(error),
) automationController {
	return automationController{
		engine:           engine,
		catalog:          catalog,
		report:           reporter,
		overflowSequence: make(map[session.Context]overflowRecovery),
		warnedTargets:    make(map[string]struct{}),
	}
}

type overflowRecovery struct {
	subject agent.Agent
	retries int
}

func (controller *automationController) release() {
	controller.mutex.Lock()
	controller.overflowSequence = make(map[session.Context]overflowRecovery)
	controller.warnedTargets = make(map[string]struct{})
	controller.mutex.Unlock()
}

func (controller *automationController) observeEvent(
	_ context.Context,
	fact plugin.Event,
) error {
	switch observed := fact.(type) {
	case agent.StatusChanged:
		if observed.Subject != nil && observed.Status == agent.StatusIdle {
			controller.resetAgent(observed.Subject)
		}
	case session.EventAppended:
		if observed.Conversation != nil &&
			observed.Committed.Type == session.AssistantMessageEventName {
			controller.resetSession(observed.Conversation)
		}
	}
	return nil
}

func (controller *automationController) interceptPressure(
	requestContext context.Context,
	notice agent.PreStepNotice,
	downstream plugin.WaterfallAction[agent.PreStepNotice, agent.PreStepDecision],
) (agent.PreStepDecision, error) {
	if downstream == nil {
		return agent.PreStepDecision{}, errors.New(
			"compaction-basic: pressure downstream is nil",
		)
	}
	if requestContext == nil || requestContext.Err() != nil ||
		notice.Subject == nil {
		return downstream.Execute(requestContext, notice)
	}
	_, err := controller.engine.CompactIfNeeded(
		requestContext,
		agentContext(notice.Subject),
		compaction.TriggerPressure,
	)
	if err != nil && controller.shouldReportPressure(err) {
		controller.report(fmt.Errorf(
			"compaction-basic: step compaction failed; continuing the turn: %w",
			err,
		))
	}
	return downstream.Execute(requestContext, notice)
}

func (controller *automationController) interceptOverflow(
	requestContext context.Context,
	notice agent.RequestErrorNotice,
	downstream plugin.WaterfallAction[agent.RequestErrorNotice, agent.RequestErrorAction],
) (agent.RequestErrorAction, error) {
	if downstream == nil {
		return agent.RequestErrorAction{}, errors.New(
			"compaction-basic: overflow downstream is nil",
		)
	}
	if requestContext == nil || requestContext.Err() != nil ||
		notice.Subject == nil ||
		notice.Failure.Code != llm.ContextWindowExceededCode {
		return downstream.Execute(requestContext, notice)
	}
	conversation := notice.Subject.SessionValue()
	selectedTarget, found, err := routedTarget(conversation)
	if err != nil {
		controller.report(fmt.Errorf(
			"compaction-basic: inspect overflow route: %w",
			err,
		))
		return downstream.Execute(requestContext, notice)
	}
	if !found {
		return downstream.Execute(requestContext, notice)
	}
	targetPolicy := controller.catalog.resolve(selectedTarget)
	retries := controller.retryCount(notice.Subject)
	if retries >= targetPolicy.MaxOverflowRetries {
		return downstream.Execute(requestContext, notice)
	}
	beforeGeneration := conversation.Surface().ReplaceGeneration
	_, recoveryErr := controller.engine.CompactIfNeeded(
		requestContext,
		agentContext(notice.Subject),
		compaction.TriggerContextOverflow,
	)
	afterGeneration := conversation.Surface().ReplaceGeneration
	if recoveryErr != nil {
		controller.report(fmt.Errorf(
			"compaction-basic: context-overflow compaction failed: %w",
			recoveryErr,
		))
	}
	if requestContext.Err() != nil || afterGeneration <= beforeGeneration {
		return downstream.Execute(requestContext, notice)
	}
	controller.recordRetry(notice.Subject, retries+1)
	return agent.RequestErrorAction{
		Retry: true,
	}, nil
}

func (controller *automationController) retryCount(subject agent.Agent) int {
	conversation := subject.SessionValue()
	controller.mutex.Lock()
	defer controller.mutex.Unlock()
	current, found := controller.overflowSequence[conversation]
	if !found || !agent.Same(current.subject, subject) {
		controller.overflowSequence[conversation] = overflowRecovery{
			subject: subject,
		}
		return 0
	}
	return current.retries
}

func (controller *automationController) recordRetry(
	subject agent.Agent,
	retries int,
) {
	conversation := subject.SessionValue()
	controller.mutex.Lock()
	controller.overflowSequence[conversation] = overflowRecovery{
		subject: subject,
		retries: retries,
	}
	controller.mutex.Unlock()
}

func (controller *automationController) resetAgent(subject agent.Agent) {
	conversation := subject.SessionValue()
	controller.mutex.Lock()
	current, found := controller.overflowSequence[conversation]
	if found && agent.Same(current.subject, subject) {
		delete(controller.overflowSequence, conversation)
	}
	controller.mutex.Unlock()
}

func (controller *automationController) resetSession(
	conversation session.Context,
) {
	controller.mutex.Lock()
	delete(controller.overflowSequence, conversation)
	controller.mutex.Unlock()
}

func (controller *automationController) shouldReportPressure(problem error) bool {
	var targetProblem *TargetPressureConfigError
	if !errors.As(problem, &targetProblem) {
		return true
	}
	controller.mutex.Lock()
	defer controller.mutex.Unlock()
	if _, reported := controller.warnedTargets[targetProblem.TargetKey]; reported {
		return false
	}
	controller.warnedTargets[targetProblem.TargetKey] = struct{}{}
	return true
}

func agentContext(subject agent.Agent) compaction.AgentContext {
	options := subject.OptionsValue()
	return compaction.AgentContext{
		Session:  subject.SessionValue(),
		Provider: options.Provider,
		Model:    options.Model,
	}
}
