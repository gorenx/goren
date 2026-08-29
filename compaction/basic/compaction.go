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
	catalog    *policyCatalog
	llmRuntime llm.LlmRuntime
	sessions   session.LiveStore
	meter      tokenmeter.Meter
	pruner     toolresultpruner.Pruner
	compactor  regionCompactor
}

func newCompaction(catalog *policyCatalog) *Compaction {
	return &Compaction{
		catalog:   catalog,
		compactor: newRegionCompactor(catalog),
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
	implementation.compactor.bind(llmRuntime, meter)
}

func (implementation *Compaction) release() {
	implementation.compactor.release()
	implementation.pruner = nil
	implementation.meter = nil
	implementation.sessions = nil
	implementation.llmRuntime = nil
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
	reading, err := readSurface(
		requestContext,
		ownerContext.Session,
		implementation.meter,
		&measurement,
	)
	if err != nil {
		return nil, err
	}
	selectedRange, err := selectRange(reading, 0)
	if err != nil || selectedRange == nil {
		return nil, err
	}
	outcome, err := implementation.CompactRegion(
		requestContext,
		selectedRange.Start,
		selectedRange.End,
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
	targetPolicy := implementation.catalog.resolve(selectedTarget)
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
	for retryIndex := 0; retryIndex <= compactSpec.CompactionRetries; retryIndex++ {
		reading, readingErr := readSurface(
			requestContext,
			ownerContext.Session,
			implementation.meter,
			&measurement,
		)
		if readingErr != nil {
			return nil, readingErr
		}
		selectedRange, rangeErr := selectRange(
			reading,
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
			selectedRange.Start,
			selectedRange.End,
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
	return implementation.compactor.compactTurn(
		requestContext,
		ownerContext,
		compaction.SurfaceRange{
			Start: start,
			End:   end,
		},
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

func assertNoActiveCompaction(
	conversation session.Context,
	stage string,
) error {
	logState, err := compaction.InspectLog(conversation.Events())
	if err != nil {
		return err
	}
	if logState.Attempt == nil {
		return nil
	}
	return &compaction.ManualError{
		Code: compaction.ManualErrorBusy,
		Message: stage + ": compaction already in progress; " +
			"the Session compaction lock is already active",
	}
}

var _ compaction.Engine = (*Compaction)(nil)
