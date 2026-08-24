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

// automaticCompaction owns cross-hook overflow recovery state. The Engine
// remains free of Event and Waterfall lifecycle state.
type automaticCompaction struct {
	engine *Compaction
	report func(error)

	mutex            sync.Mutex
	overflowSequence map[session.Context]overflowRecovery
	warnedTargets    map[string]struct{}
}

type overflowRecovery struct {
	subject agent.Agent
	retries int
}

func (automation *automaticCompaction) release() {
	automation.mutex.Lock()
	automation.overflowSequence = make(map[session.Context]overflowRecovery)
	automation.warnedTargets = make(map[string]struct{})
	automation.mutex.Unlock()
}

func (automation *automaticCompaction) observeEvent(
	_ context.Context,
	fact plugin.Event,
) error {
	switch observed := fact.(type) {
	case agent.StatusChanged:
		if observed.Subject != nil && observed.Status == agent.StatusIdle {
			automation.resetAgent(observed.Subject)
		}
	case session.EventAppended:
		if observed.Conversation != nil &&
			observed.Committed.Type == session.AssistantMessageEventName {
			automation.resetSession(observed.Conversation)
		}
	}
	return nil
}

func (automation *automaticCompaction) interceptPressure(
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
	_, err := automation.engine.CompactIfNeeded(
		requestContext,
		agentContext(notice.Subject),
		compaction.TriggerPressure,
	)
	if err != nil && automation.shouldReportPressure(err) {
		automation.report(fmt.Errorf(
			"compaction-basic: step compaction failed; continuing the turn: %w",
			err,
		))
	}
	return downstream.Execute(requestContext, notice)
}

func (automation *automaticCompaction) interceptOverflow(
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
		automation.report(fmt.Errorf(
			"compaction-basic: inspect overflow route: %w",
			err,
		))
		return downstream.Execute(requestContext, notice)
	}
	if !found {
		return downstream.Execute(requestContext, notice)
	}
	targetPolicy := ResolveTargetPolicy(
		automation.engine.policy,
		selectedTarget,
	)
	retries := automation.retryCount(notice.Subject)
	if retries >= targetPolicy.MaxOverflowRetries {
		return downstream.Execute(requestContext, notice)
	}
	beforeGeneration := conversation.Surface().ReplaceGeneration
	_, recoveryErr := automation.engine.CompactIfNeeded(
		requestContext,
		agentContext(notice.Subject),
		compaction.TriggerContextOverflow,
	)
	afterGeneration := conversation.Surface().ReplaceGeneration
	if recoveryErr != nil {
		automation.report(fmt.Errorf(
			"compaction-basic: context-overflow compaction failed: %w",
			recoveryErr,
		))
	}
	if requestContext.Err() != nil || afterGeneration <= beforeGeneration {
		return downstream.Execute(requestContext, notice)
	}
	automation.recordRetry(notice.Subject, retries+1)
	return agent.RequestErrorAction{
		Retry: true,
	}, nil
}

func (automation *automaticCompaction) retryCount(subject agent.Agent) int {
	conversation := subject.SessionValue()
	automation.mutex.Lock()
	defer automation.mutex.Unlock()
	current, found := automation.overflowSequence[conversation]
	if !found || !agent.Same(current.subject, subject) {
		automation.overflowSequence[conversation] = overflowRecovery{
			subject: subject,
		}
		return 0
	}
	return current.retries
}

func (automation *automaticCompaction) recordRetry(
	subject agent.Agent,
	retries int,
) {
	conversation := subject.SessionValue()
	automation.mutex.Lock()
	automation.overflowSequence[conversation] = overflowRecovery{
		subject: subject,
		retries: retries,
	}
	automation.mutex.Unlock()
}

func (automation *automaticCompaction) resetAgent(subject agent.Agent) {
	conversation := subject.SessionValue()
	automation.mutex.Lock()
	current, found := automation.overflowSequence[conversation]
	if found && agent.Same(current.subject, subject) {
		delete(automation.overflowSequence, conversation)
	}
	automation.mutex.Unlock()
}

func (automation *automaticCompaction) resetSession(
	conversation session.Context,
) {
	automation.mutex.Lock()
	delete(automation.overflowSequence, conversation)
	automation.mutex.Unlock()
}

func (automation *automaticCompaction) shouldReportPressure(problem error) bool {
	var targetProblem *TargetPressureConfigError
	if !errors.As(problem, &targetProblem) {
		return true
	}
	automation.mutex.Lock()
	defer automation.mutex.Unlock()
	if _, reported := automation.warnedTargets[targetProblem.TargetKey]; reported {
		return false
	}
	automation.warnedTargets[targetProblem.TargetKey] = struct{}{}
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
