package basic

import (
	"errors"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/compaction"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/llm/tokenmeter"
	"github.com/gorenx/goren/session"
)

// TransactionStage identifies the next business boundary of a selected summary.
type TransactionStage uint8

const (
	StartPending TransactionStage = iota
	SummaryPending
	SummaryOpening
	SummaryStreaming
	SummaryClosing
	CommitPending
	FlushPending
	TransactionFinished
)

// TransactionInput fixes the lifecycle and measured history of one summary.
type TransactionInput struct {
	Lifecycle   compaction.Start
	Snapshot    session.Snapshot
	Measurement tokenmeter.Measurement
	Range       compaction.SurfaceRange
	Fallback    RouteTarget
}

// Transaction owns a selected summary and its durable outcome, without I/O.
type Transaction struct {
	stage        TransactionStage
	sessionID    session.SessionID
	source       baseline
	start        *transactionStart
	started      session.Event
	summarizer   *Summarizer
	request      llm.GenerateOptions
	settings     llm.CallConfig
	pricing      messageEstimator
	write        session.IdempotentWrite
	barrier      session.WriteBarrier
	result       *compaction.Result
	failure      error
	cancellation error
}

// NewTransaction fixes history and parameters before proposing the start event.
func NewTransaction(settings ResolvedConfig, input TransactionInput, pricing messageEstimator) (*Transaction, error) {
	if pricing == nil {
		return nil, errors.New("compaction: transaction requires checkpoint pricing")
	}
	source, err := captureBaseline(input.Snapshot, input.Measurement, input.Range)
	if err != nil {
		return nil, err
	}
	start, err := newTransactionStart(input.Lifecycle, input.Range)
	if err != nil {
		return nil, err
	}
	collector := NewSummarizer(settings)
	request, err := collector.Request(input.Snapshot, source.selected.sequences, input.Fallback)
	if err != nil {
		return nil, err
	}
	return &Transaction{
		stage:      StartPending,
		sessionID:  input.Snapshot.Barrier.SessionID,
		source:     source,
		start:      start,
		summarizer: collector,
		request:    request,
		pricing:    pricing,
		write:      start,
	}, nil
}

// Stage reports the only authority for scheduling the next transaction operation.
func (subject *Transaction) Stage() TransactionStage { return subject.stage }

// Write returns the fixed proposal until its exact commit has been confirmed.
func (subject *Transaction) Write() (session.IdempotentWrite, error) {
	if subject.stage != StartPending && subject.stage != CommitPending {
		return nil, errors.New("compaction: no transaction write is pending")
	}
	return subject.write, nil
}

// Generation admits one model call after the start commit has been confirmed.
func (subject *Transaction) Generation() (llm.GenerateOptions, error) {
	if subject.stage != SummaryPending {
		return llm.GenerateOptions{}, errors.New("compaction: summary generation is not pending")
	}
	request, err := llm.CloneGenerateOptions(subject.request)
	if err != nil {
		return llm.GenerateOptions{}, err
	}
	subject.stage = SummaryOpening
	return request, nil
}

// Opened captures actual model parameters. An opening failure still needs closure.
func (subject *Transaction) Opened(settings llm.CallConfig, failure error) error {
	if subject.stage != SummaryOpening {
		return errors.New("compaction: unexpected generation opening")
	}
	if subject.cancellation != nil {
		failure = subject.cancellation
	}
	if failure == nil {
		failure = llm.ValidateCallConfig(settings)
	}
	if failure != nil {
		subject.failure = failure
		subject.stage = SummaryClosing
		return nil
	}
	subject.settings = llm.CloneCallConfig(settings)
	subject.stage = SummaryStreaming
	return nil
}

// Push consumes one output chunk after a successful opening.
func (subject *Transaction) Push(chunk llm.StreamChunk) error {
	if subject.stage != SummaryStreaming {
		return errors.New("compaction: summary is not open")
	}
	if subject.cancellation != nil {
		subject.failure = subject.cancellation
		subject.stage = SummaryClosing
		return subject.cancellation
	}
	if err := subject.summarizer.Push(chunk); err != nil {
		subject.failure = err
		subject.stage = SummaryClosing
		return err
	}
	return nil
}

// EndGeneration closes the read phase; even an unsuccessful call must be closed.
func (subject *Transaction) EndGeneration(failure error) error {
	if subject.stage != SummaryStreaming && !(subject.stage == SummaryOpening && failure != nil) {
		return errors.New("compaction: summary is not streaming")
	}
	if subject.cancellation != nil {
		failure = subject.cancellation
	}
	subject.failure = failure
	subject.stage = SummaryClosing
	return nil
}

// ConfirmClose keeps failed closure pending and creates a write only after release.
func (subject *Transaction) ConfirmClose(failure error) error {
	if subject.stage != SummaryClosing {
		return errors.New("compaction: summary closure is not pending")
	}
	if failure != nil {
		return errors.Join(subject.failure, failure)
	}
	if subject.cancellation != nil {
		subject.failure = subject.cancellation
	}
	if subject.failure == nil {
		generated, err := subject.summarizer.Result()
		if err == nil {
			var replacement regionReplacement
			replacement, err = buildReplacement(subject.start.lifecycle.CompactionID, subject.start.lifecycle.SourceCommandID, subject.source, generated, subject.settings, subject.pricing)
			if err == nil {
				subject.write, err = newTransactionCompletion(subject.started, replacement)
			}
		}
		subject.failure = err
	}
	if subject.failure != nil {
		ending, err := newTransactionEnd(subject.start.lifecycle, subject.failure)
		if err != nil {
			return err
		}
		subject.write = ending
	}
	subject.stage = CommitPending
	return nil
}

// FlushBarrier returns the exact terminal commit that manual compaction must persist.
func (subject *Transaction) FlushBarrier() (session.WriteBarrier, error) {
	if subject.stage != FlushPending {
		return session.WriteBarrier{}, errors.New("compaction: transaction flush is not pending")
	}
	return subject.barrier, nil
}

// ConfirmFlush consumes a definite persistence reply. Success requires the same
// Session and a durable prefix covering the end. A definite failure settles the
// transaction; an earlier compaction failure takes precedence over persistence.
// Transport failures must not enter this method because their outcome is unknown.
func (subject *Transaction) ConfirmFlush(durable session.WriteBarrier, failure error) error {
	if subject.stage != FlushPending {
		return errors.New("compaction: transaction flush is not pending")
	}
	if failure != nil {
		if subject.failure == nil {
			subject.failure = &compaction.ManualError{
				Code:    compaction.ManualErrorPersistence,
				Message: "manual compaction durability checkpoint failed",
				Cause:   failure,
			}
		}
		subject.stage = TransactionFinished
		return nil
	}
	if durable.SessionID != subject.barrier.SessionID || durable.NextSeq < subject.barrier.NextSeq {
		return errors.New("compaction: durable prefix does not cover the transaction")
	}
	subject.stage = TransactionFinished
	return nil
}

// Result is available only after resource, transaction and durability confirmation.
func (subject *Transaction) Result() (*compaction.Result, error) {
	if subject.stage != TransactionFinished {
		return nil, errors.New("compaction: transaction is unfinished")
	}
	if subject.cancellation != nil {
		return nil, subject.cancellation
	}
	if subject.failure != nil {
		return nil, subject.failure
	}
	if subject.result == nil {
		return nil, errors.New("compaction: successful transaction has no result")
	}
	detached := *subject.result
	detached.SourceCommandID = cloneString(detached.SourceCommandID)
	detached.ShadowedSeqs = append([]int64(nil), detached.ShadowedSeqs...)
	blocks, err := agentmessage.CloneContentBlocks(subject.result.Summary)
	if err != nil {
		return nil, err
	}
	detached.Summary = blocks
	return &detached, nil
}

// Cancel records intent without pretending an outstanding effect has settled.
// The first cause wins. Immutable pending writes are retained for confirmation.
func (subject *Transaction) Cancel(cause error) error {
	if cause == nil {
		return errors.New("compaction: cancellation requires a cause")
	}
	if subject.stage == TransactionFinished {
		return nil
	}
	if subject.cancellation == nil {
		subject.cancellation = cause
	}
	if subject.stage == SummaryPending {
		ending, err := newTransactionEnd(subject.start.lifecycle, subject.cancellation)
		if err != nil {
			return err
		}
		subject.failure = subject.cancellation
		subject.write = ending
		subject.stage = CommitPending
	}
	return nil
}
