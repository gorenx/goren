package basic

import (
	"context"
	"errors"
	"fmt"

	"github.com/gorenx/goren/compaction"
	"github.com/gorenx/goren/compaction/toolresultpruner"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/llm/tokenmeter"
	"github.com/gorenx/goren/session"
)

// Compaction implements pressure and manual use-case orchestration for one
// Runtime Scope. Automatic hook state, region transactions, and auxiliary LLM
// protocol handling belong to dedicated collaborators in the same package.
type Compaction struct {
	policy     ResolvedConfig
	llmRuntime llm.LlmRuntime
	sessions   session.LiveStore
	meter      tokenmeter.Meter
	pruner     toolresultpruner.Pruner
	regions    regionCompactor
}

func newCompaction(settings ResolvedConfig) *Compaction {
	policySnapshot := cloneResolvedConfig(settings)
	return &Compaction{
		policy:  policySnapshot,
		regions: newRegionCompactor(policySnapshot),
	}
}

func (implementation *Compaction) bind(
	llmRuntime llm.LlmRuntime,
	sessions session.LiveStore,
	meter tokenmeter.Meter,
	pruner toolresultpruner.Pruner,
) {
	implementation.llmRuntime = llmRuntime
	implementation.sessions = sessions
	implementation.meter = meter
	implementation.pruner = pruner
	implementation.regions.bind(llmRuntime, meter)
}

func (implementation *Compaction) release() {
	implementation.regions.release()
	implementation.pruner = nil
	implementation.meter = nil
	implementation.sessions = nil
	implementation.llmRuntime = nil
}

func (implementation *Compaction) automatic() bool {
	return implementation.policy.Auto
}

// CompactIfNeeded applies routed pressure or canonical context-overflow policy.
func (implementation *Compaction) CompactIfNeeded(
	requestContext context.Context,
	ownerContext compaction.AgentContext,
	selectedTrigger compaction.Trigger,
) (*compaction.Result, error) {
	if err := implementation.validateOperation(requestContext, ownerContext); err != nil {
		return nil, err
	}
	if err := compaction.ValidateTrigger(selectedTrigger); err != nil {
		return nil, err
	}
	selectedTarget, found, err := routedTarget(ownerContext.Session)
	if err != nil || !found {
		return nil, err
	}
	measurement, err := implementation.meter.Measure(
		requestContext,
		ownerContext.Session,
		nil,
	)
	if err != nil {
		return nil, err
	}

	switch selectedTrigger {
	case compaction.TriggerContextOverflow:
		return implementation.compactOverflow(
			requestContext,
			ownerContext,
			measurement,
		)
	case compaction.TriggerPressure:
		return implementation.compactPressure(
			requestContext,
			ownerContext,
			selectedTarget,
			measurement,
		)
	default:
		panic("unreachable validated compaction trigger")
	}
}

func (implementation *Compaction) compactOverflow(
	requestContext context.Context,
	ownerContext compaction.AgentContext,
	measurement tokenmeter.Measurement,
) (*compaction.Result, error) {
	measurement, err := implementation.pruneAndMeasure(
		requestContext,
		ownerContext.Session,
		measurement,
	)
	if err != nil {
		return nil, err
	}
	selectedRange, err := selectCompactableRange(
		ownerContext.Session,
		measurement,
		0,
	)
	if err != nil || selectedRange == nil {
		return nil, err
	}
	outcome, err := implementation.CompactRegion(
		requestContext,
		selectedRange.start,
		selectedRange.end,
		ownerContext,
	)
	if err != nil {
		return nil, err
	}
	return &outcome, nil
}

func (implementation *Compaction) compactPressure(
	requestContext context.Context,
	ownerContext compaction.AgentContext,
	selectedTarget RouteTarget,
	measurement tokenmeter.Measurement,
) (*compaction.Result, error) {

	modelInfo, err := implementation.llmRuntime.ResolveModelInfo(
		requestContext,
		selectedTarget.Provider,
		selectedTarget.Model,
	)
	if err != nil {
		return nil, err
	}
	if err := assertNoActiveCompaction(
		ownerContext.Session,
		"automatic pressure compaction",
	); err != nil {
		return nil, err
	}
	if modelInfo.Context == nil {
		targetKey := selectedTarget.Provider + "/" + selectedTarget.Model
		return nil, &TargetPressureConfigError{
			TargetKey: targetKey,
			Message: "compaction-basic: no context capacity for " + targetKey +
				"; configure contextWindow on that adapter model",
		}
	}
	targetPolicy := ResolveTargetPolicy(implementation.policy, selectedTarget)
	compactSpec, err := ResolveCompactSpec(
		targetPolicy,
		modelInfo.Context.ContextWindow,
	)
	if err != nil {
		return nil, err
	}
	if measurement.TotalTokens < compactSpec.ThresholdTokens {
		return nil, nil
	}
	measurement, err = implementation.pruneAndMeasure(
		requestContext,
		ownerContext.Session,
		measurement,
	)
	if err != nil {
		return nil, err
	}
	if measurement.TotalTokens < compactSpec.ThresholdTokens {
		return nil, nil
	}

	var latest *compaction.Result
	for attempt := 0; attempt <= compactSpec.CompactionRetries; attempt++ {
		selectedRange, rangeErr := selectCompactableRange(
			ownerContext.Session,
			measurement,
			compactSpec.RetainTokens,
		)
		if rangeErr != nil {
			return nil, rangeErr
		}
		if selectedRange == nil {
			if latest == nil {
				return nil, nil
			}
			break
		}
		outcome, compactErr := implementation.CompactRegion(
			requestContext,
			selectedRange.start,
			selectedRange.end,
			ownerContext,
		)
		if compactErr != nil {
			return nil, compactErr
		}
		latest = &outcome
		measurement, err = implementation.meter.Measure(
			requestContext,
			ownerContext.Session,
			nil,
		)
		if err != nil {
			return nil, err
		}
		if measurement.TotalTokens < compactSpec.ThresholdTokens {
			return latest, nil
		}
	}
	return nil, fmt.Errorf(
		"compaction still above threshold after %d compaction attempts (%d estimated tokens >= threshold %d)",
		compactSpec.CompactionRetries+1,
		measurement.TotalTokens,
		compactSpec.ThresholdTokens,
	)
}

// CompactNow reserves idle Agent maintenance and commits one standalone
// compaction bracket followed by a persistence checkpoint.
func (implementation *Compaction) CompactNow(
	requestContext context.Context,
	subject compaction.ManualAgentContext,
	sourceCommandID *string,
) (*compaction.Result, error) {
	if subject == nil {
		return nil, errors.New("compaction-basic: manual Agent is nil")
	}
	options := subject.OptionsValue()
	ownerContext := compaction.AgentContext{
		Session:  subject.SessionValue(),
		Provider: options.Provider,
		Model:    options.Model,
	}
	if err := implementation.validateOperation(
		requestContext,
		ownerContext,
	); err != nil {
		return nil, err
	}
	if sourceCommandID != nil && *sourceCommandID == "" {
		return nil, errors.New("compaction-basic: sourceCommandId is empty")
	}
	execution := &manualCompaction{
		implementation:  implementation,
		ownerContext:    ownerContext,
		sourceCommandID: cloneString(sourceCommandID),
		callerContext:   requestContext,
	}
	maintenanceErr := subject.RunMaintenance(
		requestContext,
		execution.execute,
	)
	if !execution.ran {
		return nil, &compaction.ManualError{
			Code:    compaction.ManualErrorBusy,
			Message: "manual compaction requires an idle agent with no waking queued work",
			Cause:   maintenanceErr,
		}
	}
	if execution.err != nil {
		return nil, execution.err
	}
	if maintenanceErr != nil {
		return nil, maintenanceErr
	}
	return execution.outcome, nil
}

// CompactRegion compacts one inclusive current-Surface span inside the open
// Agent turn.
func (implementation *Compaction) CompactRegion(
	requestContext context.Context,
	start int64,
	end int64,
	ownerContext compaction.AgentContext,
) (compaction.Result, error) {
	if err := implementation.validateOperation(requestContext, ownerContext); err != nil {
		return compaction.Result{}, err
	}
	return implementation.regions.compactSurfaceRegion(
		requestContext,
		ownerContext,
		surfaceSelection{
			start: start,
			end:   end,
		},
		transactionOptions{},
	)
}

func (implementation *Compaction) pruneAndMeasure(
	requestContext context.Context,
	conversation session.Context,
	current tokenmeter.Measurement,
) (tokenmeter.Measurement, error) {
	if implementation.pruner == nil {
		return current, nil
	}
	if _, err := implementation.pruner.PruneSession(
		requestContext,
		conversation,
	); err != nil {
		return tokenmeter.Measurement{}, err
	}
	return implementation.meter.Measure(requestContext, conversation, nil)
}

func (implementation *Compaction) validateOperation(
	requestContext context.Context,
	ownerContext compaction.AgentContext,
) error {
	if requestContext == nil {
		return errors.New("compaction-basic: operation Context is nil")
	}
	if err := requestContext.Err(); err != nil {
		return context.Cause(requestContext)
	}
	if err := compaction.ValidateAgentContext(ownerContext); err != nil {
		return err
	}
	if implementation.llmRuntime == nil || implementation.sessions == nil ||
		implementation.meter == nil {
		return errors.New("compaction-basic: Provider dependencies are not bound")
	}
	return nil
}

func routedTarget(
	conversation session.Context,
) (RouteTarget, bool, error) {
	headerValue, err := session.LatestRequestHeader(conversation.Events())
	if err != nil || headerValue == nil {
		return RouteTarget{}, false, err
	}
	if headerValue.Config.Provider == "" || headerValue.Config.Model == "" {
		return RouteTarget{}, false, nil
	}
	return RouteTarget{
		Provider: headerValue.Config.Provider,
		Model:    headerValue.Config.Model,
	}, true, nil
}

type manualCompaction struct {
	implementation  *Compaction
	ownerContext    compaction.AgentContext
	sourceCommandID *string
	callerContext   context.Context
	ran             bool
	outcome         *compaction.Result
	err             error
}

func (execution *manualCompaction) execute(operationContext context.Context) error {
	execution.ran = true
	measurement, err := execution.implementation.meter.Measure(
		operationContext,
		execution.ownerContext.Session,
		nil,
	)
	if err != nil {
		return execution.finish(operationContext, nil, err)
	}
	selectedRange, err := selectCompactableRange(
		execution.ownerContext.Session,
		measurement,
		0,
	)
	if err != nil || selectedRange == nil {
		return execution.finish(operationContext, nil, err)
	}
	outcome, err := execution.implementation.regions.compactSurfaceRegion(
		operationContext,
		execution.ownerContext,
		*selectedRange,
		transactionOptions{
			standalone:      true,
			selectedStable:  true,
			sourceCommandID: cloneString(execution.sourceCommandID),
			flush: func(flushContext context.Context) error {
				return execution.implementation.sessions.Flush(
					flushContext,
					execution.ownerContext.Session,
				)
			},
		},
	)
	return execution.finish(operationContext, &outcome, err)
}

func (execution *manualCompaction) finish(
	operationContext context.Context,
	outcome *compaction.Result,
	problem error,
) error {
	callerCause := context.Cause(execution.callerContext)
	operationCause := context.Cause(operationContext)
	if problem != nil && callerCause != nil && operationCause != nil &&
		errors.Is(operationCause, callerCause) {
		problem = callerCause
	}
	if problem == nil {
		execution.outcome = outcome
	}
	execution.err = problem
	return execution.err
}

var _ compaction.Engine = (*Compaction)(nil)
