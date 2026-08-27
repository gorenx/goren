package basic

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/compaction"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

type attemptStage string

const (
	summaryStage attemptStage = "summary"
	commitStage  attemptStage = "commit"
)

type attemptError struct {
	stage attemptStage
	cause error
}

func (problem *attemptError) Error() string {
	if problem == nil || problem.cause == nil {
		return "compaction-basic: compaction attempt failed"
	}
	return problem.cause.Error()
}

func (problem *attemptError) Unwrap() error {
	if problem == nil {
		return nil
	}
	return problem.cause
}

// regionAttempt retains close state independently from success so standalone
// maintenance can flush every durably closed attempt, including failures.
type regionAttempt struct {
	result  *compaction.Result
	closed  bool
	problem error
}

type attemptStart struct {
	event     session.Event
	lifecycle compaction.Start
}

type attemptOpening struct {
	requested    compaction.SurfaceRange
	compactionID compaction.ID
	ownership    attemptOwnership
}

func (opening *attemptOpening) Build(
	_ context.Context,
	snapshot session.Snapshot,
) ([]session.EventDraft, error) {
	if opening.ownership == nil {
		return nil, errors.New("compaction-basic: attempt ownership is nil")
	}
	if _, err := locateRegion(snapshot, opening.requested); err != nil {
		return nil, err
	}
	logState, err := compaction.InspectLog(snapshot.Events)
	if err != nil {
		return nil, err
	}
	if logState.Attempt != nil {
		return nil, &compaction.ManualError{
			Code: compaction.ManualErrorBusy,
			Message: "compaction: compaction already in progress; " +
				"the Session compaction lock is already active",
		}
	}
	lifecycle, err := opening.ownership.begin(logState, opening.compactionID)
	if err != nil {
		return nil, err
	}
	draft, err := session.NewEventDraft(compaction.StartEvent, lifecycle)
	if err != nil {
		return nil, err
	}
	return []session.EventDraft{draft}, nil
}

type attemptCompletion struct {
	stability   completionStability
	startEvent  session.Event
	lifecycle   compaction.Start
	replacement regionReplacement
	failure     *attemptError
}

func (completion *attemptCompletion) Build(
	_ context.Context,
	snapshot session.Snapshot,
) ([]session.EventDraft, error) {
	if completion.stability == nil {
		return nil, errors.New("compaction-basic: completion stability is nil")
	}
	if err := completion.stability.check(
		snapshot,
		completion.replacement.baseline,
	); err != nil {
		completion.failure = &attemptError{
			stage: summaryStage,
			cause: err,
		}
		return completion.buildFailure()
	}
	summarySnapshot, err := agentmessage.CloneContentBlocks(
		completion.replacement.generated.summary,
	)
	if err != nil {
		completion.failure = &attemptError{
			stage: commitStage,
			cause: err,
		}
		return completion.buildFailure()
	}
	selected := completion.replacement.baseline.selected
	summaryDraft, err := session.NewEventDraft(
		compaction.SummaryEvent,
		compaction.Summary{
			CompactionID:    completion.lifecycle.CompactionID,
			SourceCommandID: cloneString(completion.lifecycle.SourceCommandID),
			Summary:         summarySnapshot,
			RawOutput:       completion.replacement.generated.rawOutput,
			LLMStreamCall:   completion.replacement.generated.llmStreamCall,
			ShadowedRange: compaction.SurfaceRange{
				Start: selected.start,
				End:   selected.end,
			},
			ShadowedSeqs: append(
				[]int64(nil),
				selected.sequences...,
			),
			ShadowedTokenCount: completion.replacement.baseline.tokenCount,
			Provider:           completion.replacement.generated.provider,
			Model:              completion.replacement.generated.model,
			MaxTokens:          cloneInt(completion.replacement.generated.maxTokens),
			Usage:              cloneUsage(completion.replacement.generated.usage),
		},
	)
	if err != nil {
		completion.failure = &attemptError{
			stage: commitStage,
			cause: err,
		}
		return completion.buildFailure()
	}
	summarySequence := snapshot.Barrier.NextSeq
	sources := append(
		[]int64{
			completion.startEvent.Seq,
			summarySequence,
		},
		selected.sequences...,
	)
	replacementDraft, err := session.NewSurfaceEventDraft(
		session.UserMessageAdded,
		completion.replacement.checkpoint,
		session.SurfaceIntent{
			Operation: session.SurfaceReplace(
				selected.start,
				selected.end,
			),
			SourceEventSeqs: &sources,
		},
	)
	if err != nil {
		completion.failure = &attemptError{
			stage: commitStage,
			cause: err,
		}
		return completion.buildFailure()
	}
	endDraft, err := session.NewEventDraft(
		compaction.EndEvent,
		compaction.End{
			CompactionID:    completion.lifecycle.CompactionID,
			SourceCommandID: cloneString(completion.lifecycle.SourceCommandID),
			Turn:            cloneInt64(completion.lifecycle.Turn),
		},
	)
	if err != nil {
		completion.failure = &attemptError{
			stage: commitStage,
			cause: err,
		}
		return completion.buildFailure()
	}
	return []session.EventDraft{
		summaryDraft,
		replacementDraft,
		endDraft,
	}, nil
}

func (completion *attemptCompletion) buildFailure() ([]session.EventDraft, error) {
	detail := completion.failure.cause.Error()
	draft, err := session.NewEventDraft(
		compaction.EndEvent,
		compaction.End{
			CompactionID:    completion.lifecycle.CompactionID,
			SourceCommandID: cloneString(completion.lifecycle.SourceCommandID),
			Turn:            cloneInt64(completion.lifecycle.Turn),
			Error:           &detail,
		},
	)
	if err != nil {
		completion.failure = &attemptError{
			stage: commitStage,
			cause: err,
		}
		return nil, err
	}
	return []session.EventDraft{draft}, nil
}

func (owner *regionCompactor) complete(
	requestContext context.Context,
	conversation session.Context,
	started attemptStart,
	request regionRequest,
	generated regionReplacement,
) regionAttempt {
	completion := &attemptCompletion{
		stability:   request.stability,
		startEvent:  started.event,
		lifecycle:   started.lifecycle,
		replacement: generated,
	}
	receipt, commitErr := conversation.Commit(
		context.WithoutCancel(requestContext),
		completion,
	)
	closed := receiptClosesAttempt(receipt)
	if commitErr != nil {
		failure := completion.failure
		if failure == nil {
			failure = &attemptError{
				stage: commitStage,
				cause: commitErr,
			}
		}
		return regionAttempt{
			closed:  closed,
			problem: failure,
		}
	}
	if completion.failure != nil {
		return regionAttempt{
			closed:  closed,
			problem: completion.failure,
		}
	}
	result, err := resultFromReceipt(started, receipt)
	if err != nil {
		return regionAttempt{
			closed: closed,
			problem: &attemptError{
				stage: commitStage,
				cause: err,
			},
		}
	}
	return regionAttempt{
		result: &result,
		closed: closed,
	}
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

func attemptStartFromReceipt(receipt session.WriteResult) (attemptStart, error) {
	if len(receipt.Events) != 1 || receipt.Events[0].Type != compaction.StartEventName {
		return attemptStart{}, errors.New(
			"compaction-basic: start commit did not return one compaction/start Event",
		)
	}
	lifecycle, err := compaction.DecodeStart(receipt.Events[0].Data)
	if err != nil {
		return attemptStart{}, err
	}
	return attemptStart{
		event:     receipt.Events[0],
		lifecycle: lifecycle,
	}, nil
}

func resultFromReceipt(
	started attemptStart,
	receipt session.WriteResult,
) (compaction.Result, error) {
	if len(receipt.Events) != 3 ||
		receipt.Events[0].Type != compaction.SummaryEventName ||
		receipt.Events[1].Type != session.UserMessageEventName ||
		receipt.Events[2].Type != compaction.EndEventName {
		return compaction.Result{}, errors.New(
			"compaction-basic: completion commit returned an invalid Event batch",
		)
	}
	summaryValue, err := compaction.DecodeSummary(receipt.Events[0].Data)
	if err != nil {
		return compaction.Result{}, err
	}
	return compaction.Result{
		CompactionID:       started.lifecycle.CompactionID,
		SourceCommandID:    cloneString(started.lifecycle.SourceCommandID),
		StartSeq:           started.event.Seq,
		SummarySeq:         receipt.Events[0].Seq,
		EndSeq:             receipt.Events[2].Seq,
		Summary:            summaryValue.Summary,
		ShadowedRange:      summaryValue.ShadowedRange,
		ShadowedSeqs:       append([]int64(nil), summaryValue.ShadowedSeqs...),
		ShadowedTokenCount: summaryValue.ShadowedTokenCount,
	}, nil
}

func receiptClosesAttempt(receipt session.WriteResult) bool {
	if len(receipt.Events) == 0 {
		return false
	}
	return receipt.Events[len(receipt.Events)-1].Type == compaction.EndEventName
}

func newCompactionID() (compaction.ID, error) {
	var randomBytes [16]byte
	if _, err := rand.Read(randomBytes[:]); err != nil {
		return "", fmt.Errorf("compaction-basic: generate compactionId: %w", err)
	}
	randomBytes[6] = randomBytes[6]&0x0f | 0x40
	randomBytes[8] = randomBytes[8]&0x3f | 0x80
	encoded := make([]byte, 36)
	hex.Encode(encoded[0:8], randomBytes[0:4])
	encoded[8] = '-'
	hex.Encode(encoded[9:13], randomBytes[4:6])
	encoded[13] = '-'
	hex.Encode(encoded[14:18], randomBytes[6:8])
	encoded[18] = '-'
	hex.Encode(encoded[19:23], randomBytes[8:10])
	encoded[23] = '-'
	hex.Encode(encoded[24:36], randomBytes[10:16])
	return compaction.ID(encoded), nil
}

func cloneUsage(source *llm.TokenUsage) *llm.TokenUsage {
	if source == nil {
		return nil
	}
	detached := *source
	detached.CacheReadTokens = cloneInt64(source.CacheReadTokens)
	detached.CacheWriteTokens = cloneInt64(source.CacheWriteTokens)
	detached.ReasoningTokens = cloneInt64(source.ReasoningTokens)
	return &detached
}
