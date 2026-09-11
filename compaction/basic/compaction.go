package basic

import (
	"errors"
	"fmt"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/compaction"
	"github.com/gorenx/goren/llm/tokenmeter"
	"github.com/gorenx/goren/session"
)

// Mode identifies the use case, independently of model routing.
type Mode uint8

const (
	Pressure Mode = iota
	Overflow
	Manual
)

// CompactionStage identifies the next external fact needed by the business flow.
type CompactionStage uint8

const (
	CompactionIdle CompactionStage = iota
	PrunePending
	MeasurementPending
	TransactionPending
	CompactionFinished
)

// Invocation supplies one coherent measured history and its triggering context.
// ContextWindow is required only for pressure. Prune enables the optional pruner;
// manual compaction never prunes, even when this capability is available.
type Invocation struct {
	Mode            Mode
	Snapshot        session.Snapshot
	Measurement     tokenmeter.Measurement
	ContextWindow   *int
	Fallback        RouteTarget
	SourceCommandID *string
	Prune           bool
	// Cancellation suppresses new work if cancellation arrived during preparation.
	Cancellation error
}

// Outcome retains committed progress independently of a later summary failure.
type Outcome struct {
	Latest  *compaction.Result
	Changed bool
}

// Compaction owns one Agent's trigger flow and the active summary transaction.
// It consumes external facts; it performs no Session, model, or framework I/O.
type Compaction struct {
	settings     ResolvedConfig
	pricing      messageEstimator
	stage        CompactionStage
	mode         Mode
	barrier      session.WriteBarrier
	generation   uint64
	fallback     RouteTarget
	origin       *string
	spec         ResolvedCompactSpec
	attempts     int
	transaction  *Transaction
	outcome      Outcome
	failure      error
	cancellation error
}

// NewCompaction captures validated policy and pure checkpoint pricing.
func NewCompaction(settings ResolvedConfig, pricing messageEstimator) (*Compaction, error) {
	if pricing == nil {
		return nil, errors.New("compaction: checkpoint pricing is required")
	}
	return &Compaction{
		settings: cloneResolvedConfig(settings),
		pricing:  pricing,
	}, nil
}

// Stage reports the business boundary for the scheduler to fulfill.
func (subject *Compaction) Stage() CompactionStage { return subject.stage }

// Begin admits a new invocation only when previous business work is complete.
func (subject *Compaction) Begin(input Invocation) error {
	if subject.stage != CompactionIdle && subject.stage != CompactionFinished {
		return errors.New("compaction: an invocation is already active")
	}
	if input.Mode != Pressure && input.Mode != Overflow && input.Mode != Manual {
		return errors.New("compaction: invalid invocation mode")
	}
	if input.Snapshot.Barrier.SessionID == "" {
		return errors.New("compaction: Session identity is required")
	}
	if err := validateMeasurement(input.Snapshot, input.Measurement); err != nil {
		return err
	}
	if input.SourceCommandID != nil && (input.Mode != Manual || *input.SourceCommandID == "") {
		return errors.New("compaction: source command requires manual mode and a nonempty identity")
	}
	// Build the new invocation separately so invalid input cannot erase old results.
	next := Compaction{
		settings:   subject.settings,
		pricing:    subject.pricing,
		stage:      MeasurementPending,
		mode:       input.Mode,
		barrier:    input.Snapshot.Barrier,
		generation: input.Snapshot.Surface.ReplaceGeneration,
		fallback:   input.Fallback,
		origin:     cloneString(input.SourceCommandID),
	}
	if input.Cancellation != nil {
		next.cancellation = input.Cancellation
		next.finish(input.Cancellation)
		*subject = next
		return nil
	}
	target, found, err := routedTarget(input.Snapshot)
	if err != nil {
		return err
	}
	if input.Mode != Manual && (!subject.settings.Auto || !found) {
		next.stage = CompactionFinished
		*subject = next
		return nil
	}
	state, err := compaction.InspectLog(input.Snapshot.Events)
	if err != nil {
		return err
	}
	if state.Attempt != nil || (input.Mode == Manual && state.OpenTurn != nil) {
		return &compaction.ManualError{
			Code:    compaction.ManualErrorBusy,
			Message: "compaction: Session already has conflicting active work",
		}
	}
	if input.Mode == Pressure {
		if input.ContextWindow == nil {
			key := target.Provider + "/" + target.Model
			return &TargetPressureConfigError{
				TargetKey: key,
				Message:   "compaction: no context capacity for " + key,
			}
		}
		next.spec, err = ResolveCompactSpec(ResolveTargetPolicy(subject.settings, target), *input.ContextWindow)
		if err != nil {
			return err
		}
		if input.Measurement.TotalTokens < next.spec.ThresholdTokens {
			next.stage = CompactionFinished
			*subject = next
			return nil
		}
	}
	if input.Prune && input.Mode != Manual {
		next.stage = PrunePending
	} else {
		if err := next.Measured(input.Snapshot, input.Measurement); err != nil {
			return err
		}
	}
	*subject = next
	return nil
}

// Pruned consumes a definite pruning outcome, not an uncertain transport failure.
func (subject *Compaction) Pruned(snapshot session.Snapshot, failure error) error {
	if subject.stage != PrunePending {
		return errors.New("compaction: pruning is not pending")
	}
	if err := subject.observe(snapshot); err != nil {
		return err
	}
	if subject.cancellation != nil {
		failure = subject.cancellation
	}
	if failure != nil {
		subject.finish(failure)
	} else {
		subject.stage = MeasurementPending
	}
	return nil
}

// Measured selects the next transaction or ends the invocation using fresh prices.
func (subject *Compaction) Measured(snapshot session.Snapshot, prices tokenmeter.Measurement) error {
	if subject.stage != MeasurementPending {
		return errors.New("compaction: measurement is not pending")
	}
	if err := validateMeasurement(snapshot, prices); err != nil {
		return err
	}
	if err := subject.observe(snapshot); err != nil {
		return err
	}
	retain := int64(0)
	if subject.mode == Pressure {
		if prices.TotalTokens < subject.spec.ThresholdTokens {
			subject.finish(nil)
			return nil
		}
		if subject.attempts > subject.spec.CompactionRetries {
			subject.finish(fmt.Errorf("compaction still above threshold after %d compaction attempts (%d estimated tokens >= threshold %d)", subject.attempts, prices.TotalTokens, subject.spec.ThresholdTokens))
			return nil
		}
		retain = subject.spec.RetainTokens
	}
	selected, err := selectRange(snapshot, prices, retain)
	if err != nil {
		subject.finish(err)
		return nil
	}
	if selected == nil {
		if subject.mode == Pressure && subject.attempts > 0 {
			subject.finish(errors.New("compaction: pressure remains above threshold with no safe range"))
		} else {
			subject.finish(nil)
		}
		return nil
	}
	state, err := compaction.InspectLog(snapshot.Events)
	if err != nil {
		subject.finish(err)
		return nil
	}
	identifier, err := newCompactionID()
	if err != nil {
		subject.finish(err)
		return nil
	}
	lifecycle := compaction.Start{
		CompactionID:    identifier,
		SourceCommandID: subject.origin,
	}
	if subject.mode != Manual {
		if state.OpenTurn == nil {
			subject.finish(errors.New("compaction: automatic summary requires an open Turn"))
			return nil
		}
		lifecycle.Turn = state.OpenTurn
	}
	execution, err := NewTransaction(
		subject.settings,
		TransactionInput{
			Lifecycle:   lifecycle,
			Snapshot:    snapshot,
			Measurement: prices,
			Range:       *selected,
			Fallback:    subject.fallback,
		},
		subject.pricing,
	)
	if err != nil {
		subject.finish(err)
		return nil
	}
	subject.transaction = execution
	subject.attempts++
	subject.stage = TransactionPending
	return nil
}

// RejectMeasurement settles a definite measurement failure after earlier work
// has settled. Previously committed progress remains available in Result.
// Unknown delivery outcomes are not measurement rejections.
func (subject *Compaction) RejectMeasurement(failure error) error {
	if subject.stage != MeasurementPending {
		return errors.New("compaction: measurement is not pending")
	}
	if failure == nil {
		return errors.New("compaction: measurement rejection requires a cause")
	}
	subject.finish(failure)
	return nil
}

// CurrentTransaction exposes the active business transaction for scheduled delivery.
func (subject *Compaction) CurrentTransaction() (*Transaction, error) {
	if subject.stage != TransactionPending {
		return nil, errors.New("compaction: no summary transaction is pending")
	}
	return subject.transaction, nil
}

// CompleteTransaction requires confirmed resource closure and any manual flush.
func (subject *Compaction) CompleteTransaction(snapshot session.Snapshot) error {
	if subject.stage != TransactionPending || subject.transaction.Stage() != TransactionFinished {
		return errors.New("compaction: summary transaction is unfinished")
	}
	if snapshot.Barrier.NextSeq < subject.transaction.barrier.NextSeq {
		return errors.New("compaction: snapshot precedes transaction outcome")
	}
	if err := subject.observe(snapshot); err != nil {
		return err
	}
	completed, failure := subject.transaction.Result()
	subject.transaction = nil
	if completed != nil {
		subject.outcome.Latest = completed
	}
	if failure != nil || subject.mode != Pressure {
		subject.finish(failure)
	} else {
		subject.stage = MeasurementPending
	}
	return nil
}

// Result reports committed progress even when a later business operation failed.
func (subject *Compaction) Result() (Outcome, error) {
	if subject.stage != CompactionFinished {
		return Outcome{}, errors.New("compaction: invocation is unfinished")
	}
	detached := subject.outcome
	if detached.Latest != nil {
		copied := *detached.Latest
		copied.SourceCommandID = cloneString(copied.SourceCommandID)
		copied.ShadowedSeqs = append([]int64(nil), copied.ShadowedSeqs...)
		blocks, err := agentmessage.CloneContentBlocks(copied.Summary)
		if err != nil {
			return Outcome{}, err
		}
		copied.Summary = blocks
		detached.Latest = &copied
	}
	if subject.cancellation != nil {
		return detached, subject.cancellation
	}
	return detached, subject.failure
}

func (subject *Compaction) finish(failure error) {
	subject.failure = failure
	subject.stage = CompactionFinished
}

func (subject *Compaction) observe(snapshot session.Snapshot) error {
	if snapshot.Barrier.SessionID != subject.barrier.SessionID || snapshot.Barrier.NextSeq < subject.barrier.NextSeq || snapshot.Barrier.NextSeq != int64(len(snapshot.Events)) || snapshot.Surface.ReplaceGeneration < subject.generation {
		return errors.New("compaction: incompatible Session snapshot")
	}
	subject.barrier = snapshot.Barrier
	subject.outcome.Changed = snapshot.Surface.ReplaceGeneration > subject.generation
	return nil
}

func routedTarget(snapshot session.Snapshot) (RouteTarget, bool, error) {
	header, err := session.LatestRequestHeader(snapshot.Events)
	if err != nil || header == nil {
		return RouteTarget{}, false, err
	}
	if header.Config.Provider == "" || header.Config.Model == "" {
		return RouteTarget{}, false, nil
	}
	return RouteTarget{
		Provider: header.Config.Provider,
		Model:    header.Config.Model,
	}, true, nil
}

// CapacityTarget identifies the model metadata needed before a pressure invocation.
// Other modes and unrouted or disabled automatic work need no capacity lookup.
func (subject *Compaction) CapacityTarget(selectedMode Mode, snapshot session.Snapshot) (*RouteTarget, error) {
	if selectedMode != Pressure && selectedMode != Overflow && selectedMode != Manual {
		return nil, errors.New("compaction: invalid invocation mode")
	}
	if selectedMode != Pressure || !subject.settings.Auto {
		return nil, nil
	}
	target, found, err := routedTarget(snapshot)
	if err != nil || !found {
		return nil, err
	}
	return &target, nil
}

// Cancel suppresses further work while retaining in-flight transaction ownership.
func (subject *Compaction) Cancel(cause error) error {
	if cause == nil {
		return errors.New("compaction: cancellation requires a cause")
	}
	if subject.stage == CompactionIdle || subject.stage == CompactionFinished {
		return nil
	}
	if subject.cancellation == nil {
		subject.cancellation = cause
	}
	switch subject.stage {
	case TransactionPending:
		return subject.transaction.Cancel(subject.cancellation)
	case MeasurementPending:
		subject.finish(subject.cancellation)
	}
	return nil
}
