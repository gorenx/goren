package basic

import (
	"context"
	"errors"

	"github.com/gorenx/goren/compaction"
	"github.com/gorenx/goren/llm/tokenmeter"
	"github.com/gorenx/goren/session"
)

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
	execution := &maintenanceRun{
		sessions:        implementation.sessions,
		meter:           implementation.meter,
		compactor:       &implementation.compactor,
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
	return execution.result, nil
}

type maintenanceRun struct {
	sessions        session.LiveStore
	meter           tokenmeter.Meter
	compactor       *regionCompactor
	ownerContext    compaction.AgentContext
	sourceCommandID *string
	callerContext   context.Context
	ran             bool
	result          *compaction.Result
	err             error
}

func (execution *maintenanceRun) execute(operationContext context.Context) error {
	execution.ran = true
	if err := contextFailure(operationContext); err != nil {
		return execution.finish(
			operationContext,
			nil,
			manualCancellation(err),
		)
	}
	reading, err := readSurface(
		operationContext,
		execution.ownerContext.Session,
		execution.meter,
		nil,
	)
	if err != nil {
		if cancelErr := contextFailure(operationContext); cancelErr != nil {
			err = manualCancellation(cancelErr)
		}
		return execution.finish(operationContext, nil, err)
	}
	selectedRange, err := selectRange(reading, 0)
	if err != nil || selectedRange == nil {
		return execution.finish(operationContext, nil, err)
	}
	executed := execution.compactor.compactIdle(
		operationContext,
		execution.ownerContext,
		*selectedRange,
		execution.sourceCommandID,
	)
	var flushErr error
	if executed.closed {
		flushErr = execution.sessions.Flush(
			context.WithoutCancel(operationContext),
			execution.ownerContext.Session,
		)
	}
	if cancelErr := contextFailure(operationContext); cancelErr != nil {
		return execution.finish(
			operationContext,
			nil,
			manualCancellation(cancelErr),
		)
	}
	if executed.problem != nil {
		return execution.finish(
			operationContext,
			nil,
			classifyManualFailure(executed.problem),
		)
	}
	if flushErr != nil {
		return execution.finish(
			operationContext,
			nil,
			&compaction.ManualError{
				Code:    compaction.ManualErrorPersistence,
				Message: "manual compaction durability checkpoint failed",
				Cause:   flushErr,
			},
		)
	}
	if executed.result == nil {
		return execution.finish(
			operationContext,
			nil,
			errors.New("compaction-basic: completed manual compaction has no result"),
		)
	}
	return execution.finish(operationContext, executed.result, nil)
}

func (execution *maintenanceRun) finish(
	operationContext context.Context,
	result *compaction.Result,
	problem error,
) error {
	callerCause := context.Cause(execution.callerContext)
	operationCause := context.Cause(operationContext)
	if problem != nil && callerCause != nil && operationCause != nil &&
		errors.Is(operationCause, callerCause) {
		problem = callerCause
	}
	if problem == nil {
		execution.result = result
	}
	execution.err = problem
	return execution.err
}

func classifyManualFailure(problem error) error {
	var failure *attemptError
	if !errors.As(problem, &failure) {
		return problem
	}
	if failure.stage == commitStage {
		return &compaction.ManualError{
			Code:    compaction.ManualErrorCommit,
			Message: "manual compaction did not commit cleanly",
			Cause:   failure.cause,
		}
	}
	var changed *surfaceChangedError
	if errors.As(failure.cause, &changed) {
		return &compaction.ManualError{
			Code:    compaction.ManualErrorChanged,
			Message: "the compacted history changed during manual compaction",
			Cause:   failure.cause,
		}
	}
	return &compaction.ManualError{
		Code:    compaction.ManualErrorSummary,
		Message: "manual compaction could not produce a smaller summary",
		Cause:   failure.cause,
	}
}

func manualCancellation(cause error) error {
	return &compaction.ManualError{
		Code:    compaction.ManualErrorCancelled,
		Message: "manual compaction was cancelled",
		Cause:   cause,
	}
}
