package basic

import (
	"context"
	"errors"

	"github.com/gorenx/goren/compaction"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/llm/tokenmeter"
	"github.com/gorenx/goren/session"
)

// regionCompactor owns the durable start/summarize/replace/end sequence. It
// does not own trigger policy, persistence checkpoints, or manual error mapping.
type regionCompactor struct {
	meter      tokenmeter.Meter
	summarizer llmSummarizer
}

type regionRequest struct {
	requested compaction.SurfaceRange
	ownership attemptOwnership
	stability completionStability
}

// attemptOwnership creates the durable lifecycle owner after inspecting the
// same Session snapshot that will accept compaction/start.
type attemptOwnership interface {
	begin(compaction.LogState, compaction.ID) (compaction.Start, error)
}

// completionStability protects the history assumptions made while a summary
// was generated. Ownership and stability are independent transaction rules.
type completionStability interface {
	check(session.Snapshot, baseline) error
}

type turnOwnership struct{}

func (turnOwnership) begin(
	logState compaction.LogState,
	compactionID compaction.ID,
) (compaction.Start, error) {
	if logState.OpenTurn == nil {
		return compaction.Start{}, errors.New(
			"compactRegion: no open turn; automatic compaction events must be enclosed in a turn",
		)
	}
	turnOwner := *logState.OpenTurn
	return compaction.Start{
		CompactionID: compactionID,
		Turn:         &turnOwner,
	}, nil
}

type wholeSurfaceStability struct{}

func (wholeSurfaceStability) check(
	snapshot session.Snapshot,
	source baseline,
) error {
	return assertWholeSurfaceStable(snapshot, source)
}

type standaloneOwnership struct {
	sourceCommandID *string
}

func (ownership standaloneOwnership) begin(
	logState compaction.LogState,
	compactionID compaction.ID,
) (compaction.Start, error) {
	if logState.OpenTurn != nil {
		return compaction.Start{}, &compaction.ManualError{
			Code:    compaction.ManualErrorBusy,
			Message: "manual compaction: the Session already has an open turn",
		}
	}
	return compaction.Start{
		CompactionID:    compactionID,
		SourceCommandID: cloneString(ownership.sourceCommandID),
		Turn:            nil,
	}, nil
}

type selectedRegionStability struct {
	requestContext context.Context
}

func (stability selectedRegionStability) check(
	snapshot session.Snapshot,
	source baseline,
) error {
	if err := contextFailure(stability.requestContext); err != nil {
		return err
	}
	return assertSelectedRegionStable(snapshot, source)
}

func contextFailure(requestContext context.Context) error {
	if requestContext == nil {
		return errors.New("compaction-basic: operation Context is nil")
	}
	if requestContext.Err() == nil {
		return nil
	}
	if cause := context.Cause(requestContext); cause != nil {
		return cause
	}
	return requestContext.Err()
}

var _ attemptOwnership = turnOwnership{}
var _ attemptOwnership = standaloneOwnership{}
var _ completionStability = wholeSurfaceStability{}
var _ completionStability = selectedRegionStability{}

func newRegionCompactor(catalog *policyCatalog) regionCompactor {
	return regionCompactor{
		summarizer: newLLMSummarizer(catalog),
	}
}

func (owner *regionCompactor) bind(
	llmRuntime llm.LlmRuntime,
	pricing tokenmeter.Meter,
) {
	owner.meter = pricing
	owner.summarizer.bind(llmRuntime)
}

func (owner *regionCompactor) release() {
	owner.summarizer.release()
	owner.meter = nil
}

func (owner *regionCompactor) compactTurn(
	requestContext context.Context,
	ownerContext compaction.AgentContext,
	requested compaction.SurfaceRange,
) (compaction.Result, error) {
	executed := owner.compact(
		requestContext,
		ownerContext,
		regionRequest{
			requested: requested,
			ownership: turnOwnership{},
			stability: wholeSurfaceStability{},
		},
	)
	if executed.problem != nil {
		var failed *attemptError
		if errors.As(executed.problem, &failed) {
			return compaction.Result{}, failed.cause
		}
		return compaction.Result{}, executed.problem
	}
	if executed.result == nil {
		return compaction.Result{}, errors.New(
			"compaction-basic: completed turn compaction has no result",
		)
	}
	return *executed.result, nil
}

func (owner *regionCompactor) compactIdle(
	requestContext context.Context,
	ownerContext compaction.AgentContext,
	requested compaction.SurfaceRange,
	sourceCommandID *string,
) regionAttempt {
	return owner.compact(
		requestContext,
		ownerContext,
		regionRequest{
			requested: requested,
			ownership: standaloneOwnership{
				sourceCommandID: cloneString(sourceCommandID),
			},
			stability: selectedRegionStability{
				requestContext: requestContext,
			},
		},
	)
}

func (owner *regionCompactor) close(
	requestContext context.Context,
	conversation session.Context,
	lifecycle compaction.Start,
	failure attemptError,
) regionAttempt {
	detail := failure.cause.Error()
	draft, err := session.NewEventDraft(
		compaction.EndEvent,
		compaction.End{
			CompactionID:    lifecycle.CompactionID,
			SourceCommandID: cloneString(lifecycle.SourceCommandID),
			Turn:            cloneInt64(lifecycle.Turn),
			Error:           &detail,
		},
	)
	if err != nil {
		return regionAttempt{
			problem: &attemptError{
				stage: commitStage,
				cause: err,
			},
		}
	}
	receipt, commitErr := conversation.Commit(
		context.WithoutCancel(requestContext),
		session.Batch(draft),
	)
	closed := receiptClosesAttempt(receipt)
	if commitErr != nil {
		return regionAttempt{
			closed: closed,
			problem: &attemptError{
				stage: commitStage,
				cause: commitErr,
			},
		}
	}
	return regionAttempt{
		closed:  closed,
		problem: &failure,
	}
}

func (owner *regionCompactor) compact(
	requestContext context.Context,
	ownerContext compaction.AgentContext,
	request regionRequest,
) regionAttempt {
	compactionID, err := newCompactionID()
	if err != nil {
		return regionAttempt{
			problem: err,
		}
	}
	opening := &attemptOpening{
		requested:    request.requested,
		compactionID: compactionID,
		ownership:    request.ownership,
	}
	receipt, commitErr := ownerContext.Session.Commit(requestContext, opening)
	started, startErr := attemptStartFromReceipt(receipt)
	if commitErr != nil {
		if startErr == nil {
			return owner.close(
				requestContext,
				ownerContext.Session,
				started.lifecycle,
				attemptError{
					stage: commitStage,
					cause: commitErr,
				},
			)
		}
		return regionAttempt{
			problem: commitErr,
		}
	}
	if startErr != nil {
		return regionAttempt{
			problem: startErr,
		}
	}
	source, err := owner.loadBaseline(
		requestContext,
		ownerContext.Session,
		request.requested,
	)
	if err != nil {
		return owner.close(
			requestContext,
			ownerContext.Session,
			started.lifecycle,
			attemptError{
				stage: summaryStage,
				cause: err,
			},
		)
	}
	generated, err := owner.buildReplacement(
		requestContext,
		ownerContext,
		compactionID,
		started.lifecycle.SourceCommandID,
		source,
	)
	if err != nil {
		return owner.close(
			requestContext,
			ownerContext.Session,
			started.lifecycle,
			attemptError{
				stage: summaryStage,
				cause: err,
			},
		)
	}
	return owner.complete(
		requestContext,
		ownerContext.Session,
		started,
		request,
		generated,
	)
}
