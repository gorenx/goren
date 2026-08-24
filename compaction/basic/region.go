package basic

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"

	"github.com/gorenx/goren/compaction"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/llm/tokenmeter"
	"github.com/gorenx/goren/session"
)

// regionCompactor owns the durable start/summarize/replace/end transaction.
// It has no automatic-trigger or retry state.
type regionCompactor struct {
	meter      tokenmeter.Meter
	summarizer llmSummarizer
}

func newRegionCompactor(settings ResolvedConfig) regionCompactor {
	return regionCompactor{
		summarizer: newLLMSummarizer(settings),
	}
}

func (coordinator *regionCompactor) bind(
	llmRuntime llm.LlmRuntime,
	meter tokenmeter.Meter,
) {
	coordinator.meter = meter
	coordinator.summarizer.bind(llmRuntime)
}

func (coordinator *regionCompactor) release() {
	coordinator.summarizer.release()
	coordinator.meter = nil
}

type surfaceSelection struct {
	start        int64
	end          int64
	startIndex   int
	endIndex     int
	shadowedSeqs []int64
}

type preparedCompaction struct {
	surfaceSelection
	measurement        tokenmeter.Measurement
	selectedNodes      []tokenmeter.SurfaceNode
	shadowedTokenCount int64
	input              summarizationInput
}

type summarizedCompaction struct {
	preparedCompaction
	generated  summaryResult
	checkpoint llm.UserMessage
}

type transactionOptions struct {
	standalone      bool
	selectedStable  bool
	sourceCommandID *string
	flush           func(context.Context) error
}

type transactionFailure struct {
	stage transactionStage
	err   error
}

type transactionStage string

const (
	transactionSummary transactionStage = "summary"
	transactionCommit  transactionStage = "commit"
)

type surfaceChangedError struct {
	message string
	cause   error
}

func (problem *surfaceChangedError) Error() string {
	if problem == nil {
		return "compaction: Surface changed"
	}
	return problem.message
}

func (problem *surfaceChangedError) Unwrap() error {
	if problem == nil {
		return nil
	}
	return problem.cause
}

func selectCompactableRange(
	conversation *session.Session,
	measurement tokenmeter.Measurement,
	retainTokens int64,
) (*surfaceSelection, error) {
	if conversation == nil {
		return nil, errors.New("compaction-basic: selection Session is nil")
	}
	if retainTokens < 0 {
		return nil, errors.New("compaction-basic: retained token budget is negative")
	}
	pricedNodes := measurement.Nodes
	if len(pricedNodes) == 0 {
		return nil, nil
	}
	_, surface := conversation.ReadCut()
	if len(surface.Nodes) != len(pricedNodes) {
		return nil, errors.New(
			"compaction: token-meter Surface does not match the current Session Surface",
		)
	}
	for nodeIndex, pricedNode := range pricedNodes {
		if surface.Nodes[nodeIndex] != pricedNode.Seq {
			return nil, errors.New(
				"compaction: token-meter Surface does not match the current Session Surface",
			)
		}
	}
	accumulated := int64(0)
	keepFromIndex := len(pricedNodes)
	for nodeIndex := len(pricedNodes) - 1; nodeIndex >= 0; nodeIndex-- {
		if pricedNodes[nodeIndex].Tokens < 0 ||
			pricedNodes[nodeIndex].Tokens > int64(1<<53-1)-accumulated {
			return nil, errors.New("compaction-basic: invalid retained-tail token price")
		}
		accumulated += pricedNodes[nodeIndex].Tokens
		keepFromIndex = nodeIndex
		if accumulated >= retainTokens {
			break
		}
	}
	if keepFromIndex == 0 {
		return nil, nil
	}
	for keepFromIndex > 0 {
		balanced, err := compaction.ToolPairingBalancedBefore(
			conversation,
			surface.Nodes[keepFromIndex],
		)
		if err != nil {
			return nil, err
		}
		if balanced {
			break
		}
		keepFromIndex--
	}
	if keepFromIndex == 0 {
		return nil, nil
	}
	return &surfaceSelection{
		start: surface.Nodes[0],
		end:   surface.Nodes[keepFromIndex-1],
	}, nil
}

func (coordinator *regionCompactor) compactSurfaceRegion(
	requestContext context.Context,
	ownerContext compaction.AgentContext,
	requested surfaceSelection,
	settings transactionOptions,
) (compaction.Result, error) {
	if settings.standalone {
		if err := contextFailure(requestContext); err != nil {
			return compaction.Result{}, manualCancellation(err)
		}
	}
	compactionID, err := newCompactionID()
	if err != nil {
		return compaction.Result{}, err
	}
	var selected surfaceSelection
	var startEvent session.Event
	var turnOwner *int64
	var lifecycle compaction.Start
	err = session.SerializeProducer(ownerContext.Session, func() error {
		validated, validateErr := validateSurfaceRegion(
			ownerContext.Session,
			requested.start,
			requested.end,
		)
		if validateErr != nil {
			return validateErr
		}
		logState, inspectErr := compaction.InspectLog(ownerContext.Session.Events())
		if inspectErr != nil {
			return inspectErr
		}
		if logState.Attempt != nil {
			return &compaction.ManualError{
				Code: compaction.ManualErrorBusy,
				Message: "compaction: compaction already in progress; " +
					"the Session compaction lock is already active",
			}
		}
		if settings.standalone {
			if logState.OpenTurn != nil {
				return &compaction.ManualError{
					Code:    compaction.ManualErrorBusy,
					Message: "manual compaction: the Session already has an open turn",
				}
			}
			turnOwner = nil
		} else {
			if logState.OpenTurn == nil {
				return errors.New(
					"compactRegion: no open turn; automatic compaction events must be enclosed in a turn",
				)
			}
			ownerValue := *logState.OpenTurn
			turnOwner = &ownerValue
		}
		selected = validated
		lifecycle = compaction.Start{
			CompactionID:    compactionID,
			SourceCommandID: cloneString(settings.sourceCommandID),
			Turn:            cloneInt64(turnOwner),
		}
		startEvent, validateErr = session.Append(
			ownerContext.Session,
			compaction.StartEvent,
			lifecycle,
		)
		return validateErr
	})
	if err != nil {
		return compaction.Result{}, err
	}

	prepared, err := coordinator.prepareCompaction(
		requestContext,
		ownerContext.Session,
		selected,
	)
	if err != nil {
		return coordinator.failTransaction(
			requestContext,
			ownerContext.Session,
			lifecycle,
			settings,
			transactionFailure{
				stage: transactionSummary,
				err:   err,
			},
		)
	}
	summarized, err := coordinator.summarizeCompaction(
		requestContext,
		ownerContext,
		compactionID,
		settings.sourceCommandID,
		prepared,
	)
	if err != nil {
		return coordinator.failTransaction(
			requestContext,
			ownerContext.Session,
			lifecycle,
			settings,
			transactionFailure{
				stage: transactionSummary,
				err:   err,
			},
		)
	}
	if settings.standalone {
		if err := contextFailure(requestContext); err != nil {
			return coordinator.failTransaction(
				requestContext,
				ownerContext.Session,
				lifecycle,
				settings,
				transactionFailure{
					stage: transactionSummary,
					err:   err,
				},
			)
		}
	}
	outcome, closed, transactionErr := coordinator.commitCompaction(
		requestContext,
		ownerContext.Session,
		startEvent,
		lifecycle,
		settings,
		summarized,
	)
	flushErr := error(nil)
	if closed && settings.flush != nil {
		flushErr = settings.flush(context.WithoutCancel(requestContext))
	}
	if settings.standalone {
		if cancelErr := contextFailure(requestContext); cancelErr != nil {
			return compaction.Result{}, manualCancellation(cancelErr)
		}
	}
	if transactionErr != nil {
		return compaction.Result{}, classifyTransactionFailure(
			settings.standalone,
			transactionErr,
		)
	}
	if flushErr != nil {
		return compaction.Result{}, &compaction.ManualError{
			Code:    compaction.ManualErrorPersistence,
			Message: "manual compaction durability checkpoint failed",
			Cause:   flushErr,
		}
	}
	return outcome, nil
}

func validateSurfaceRegion(
	conversation *session.Session,
	start int64,
	end int64,
) (surfaceSelection, error) {
	_, surface := conversation.ReadCut()
	startIndex := indexOfSequence(surface.Nodes, start)
	if startIndex < 0 {
		return surfaceSelection{}, fmt.Errorf(
			"compactRegion: start seq %d not found in Surface",
			start,
		)
	}
	endIndex := indexOfSequence(surface.Nodes, end)
	if endIndex < 0 {
		return surfaceSelection{}, fmt.Errorf(
			"compactRegion: end seq %d not found in Surface",
			end,
		)
	}
	if startIndex > endIndex {
		return surfaceSelection{}, fmt.Errorf(
			"compactRegion: start seq %d (position %d) is after end seq %d (position %d) on the Surface",
			start,
			startIndex,
			end,
			endIndex,
		)
	}
	balancedBefore, err := compaction.ToolPairingBalancedBefore(
		conversation,
		start,
	)
	if err != nil {
		return surfaceSelection{}, err
	}
	if !balancedBefore {
		return surfaceSelection{}, fmt.Errorf(
			"compactRegion: start seq %d is not a balanced boundary (would split a step's tool-call/result pair)",
			start,
		)
	}
	balancedAfter, err := compaction.ToolPairingBalancedAfter(
		conversation,
		end,
	)
	if err != nil {
		return surfaceSelection{}, err
	}
	if !balancedAfter {
		return surfaceSelection{}, fmt.Errorf(
			"compactRegion: end seq %d is not a balanced boundary (would split a step, or the step is still open)",
			end,
		)
	}
	return surfaceSelection{
		start:        start,
		end:          end,
		startIndex:   startIndex,
		endIndex:     endIndex,
		shadowedSeqs: append([]int64(nil), surface.Nodes[startIndex:endIndex+1]...),
	}, nil
}

func (coordinator *regionCompactor) prepareCompaction(
	requestContext context.Context,
	conversation *session.Session,
	selected surfaceSelection,
) (preparedCompaction, error) {
	measurement, err := coordinator.meter.Measure(
		requestContext,
		conversation,
		nil,
	)
	if err != nil {
		return preparedCompaction{}, err
	}
	if selected.startIndex < 0 || selected.endIndex >= len(measurement.Nodes) {
		return preparedCompaction{}, &surfaceChangedError{
			message: "compaction: selected Surface changed before summarization began",
		}
	}
	selectedNodes := append(
		[]tokenmeter.SurfaceNode(nil),
		measurement.Nodes[selected.startIndex:selected.endIndex+1]...,
	)
	if len(selectedNodes) != len(selected.shadowedSeqs) {
		return preparedCompaction{}, &surfaceChangedError{
			message: "compaction: selected Surface changed before summarization began",
		}
	}
	shadowedTokenCount := int64(0)
	for nodeIndex, pricedNode := range selectedNodes {
		if pricedNode.Seq != selected.shadowedSeqs[nodeIndex] {
			return preparedCompaction{}, &surfaceChangedError{
				message: "compaction: selected Surface changed before summarization began",
			}
		}
		if pricedNode.Tokens < 0 ||
			pricedNode.Tokens > int64(1<<53-1)-shadowedTokenCount {
			return preparedCompaction{}, errors.New(
				"compaction-basic: invalid shadowed token price",
			)
		}
		shadowedTokenCount += pricedNode.Tokens
	}
	input, err := buildSummarizationInput(conversation, selected.shadowedSeqs)
	if err != nil {
		return preparedCompaction{}, err
	}
	return preparedCompaction{
		surfaceSelection:   selected,
		measurement:        measurement,
		selectedNodes:      selectedNodes,
		shadowedTokenCount: shadowedTokenCount,
		input:              input,
	}, nil
}

func (coordinator *regionCompactor) summarizeCompaction(
	requestContext context.Context,
	ownerContext compaction.AgentContext,
	compactionID compaction.ID,
	sourceCommandID *string,
	prepared preparedCompaction,
) (summarizedCompaction, error) {
	generated, err := coordinator.summarizer.summarize(
		requestContext,
		prepared.input,
		ownerContext,
	)
	if err != nil {
		return summarizedCompaction{}, err
	}
	origin, err := compaction.NewCheckpointSource(
		compactionID,
		sourceCommandID,
	)
	if err != nil {
		return summarizedCompaction{}, err
	}
	framedSummary, err := frameSummary(generated.summary)
	if err != nil {
		return summarizedCompaction{}, err
	}
	checkpoint, err := llm.NewUserMessage(llm.UserMessageInput{
		Content: framedSummary,
		Source:  origin,
	})
	if err != nil {
		return summarizedCompaction{}, err
	}
	checkpointTokens, err := coordinator.meter.EstimateMessage(checkpoint)
	if err != nil {
		return summarizedCompaction{}, err
	}
	if checkpointTokens >= prepared.shadowedTokenCount {
		return summarizedCompaction{}, fmt.Errorf(
			"summary is not smaller than the shadowed content (%d estimated framed tokens >= %d)",
			checkpointTokens,
			prepared.shadowedTokenCount,
		)
	}
	return summarizedCompaction{
		preparedCompaction: prepared,
		generated:          generated,
		checkpoint:         checkpoint,
	}, nil
}

func (coordinator *regionCompactor) commitCompaction(
	requestContext context.Context,
	conversation *session.Session,
	startEvent session.Event,
	lifecycle compaction.Start,
	settings transactionOptions,
	summarized summarizedCompaction,
) (compaction.Result, bool, *transactionFailure) {
	var outcome compaction.Result
	closed := false
	var failure *transactionFailure
	serializedErr := session.SerializeProducer(conversation, func() error {
		if err := contextFailure(requestContext); err != nil {
			failure = &transactionFailure{
				stage: transactionSummary,
				err:   err,
			}
			closed, failure = closeFailedAttempt(
				conversation,
				lifecycle,
				failure,
			)
			return nil
		}
		stabilityErr := coordinator.assertStable(
			requestContext,
			conversation,
			summarized.preparedCompaction,
			settings.selectedStable,
		)
		if stabilityErr != nil {
			failure = &transactionFailure{
				stage: transactionSummary,
				err:   stabilityErr,
			}
			closed, failure = closeFailedAttempt(
				conversation,
				lifecycle,
				failure,
			)
			return nil
		}
		summarySnapshot, err := llm.CloneContentBlocks(
			summarized.generated.summary,
		)
		if err != nil {
			failure = &transactionFailure{
				stage: transactionCommit,
				err:   err,
			}
			closed, failure = closeFailedAttempt(
				conversation,
				lifecycle,
				failure,
			)
			return nil
		}
		summaryEvent, err := session.Append(
			conversation,
			compaction.SummaryEvent,
			compaction.Summary{
				CompactionID:    lifecycle.CompactionID,
				SourceCommandID: cloneString(lifecycle.SourceCommandID),
				Summary:         summarySnapshot,
				RawOutput:       summarized.generated.rawOutput,
				LLMStreamCall:   summarized.generated.llmStreamCall,
				ShadowedRange: compaction.SurfaceRange{
					Start: summarized.start,
					End:   summarized.end,
				},
				ShadowedSeqs:       append([]int64(nil), summarized.shadowedSeqs...),
				ShadowedTokenCount: summarized.shadowedTokenCount,
				Provider:           summarized.generated.provider,
				Model:              summarized.generated.model,
				MaxTokens:          cloneInt(summarized.generated.maxTokens),
				Usage:              cloneUsage(summarized.generated.usage),
			},
		)
		if err != nil {
			failure = &transactionFailure{
				stage: transactionCommit,
				err:   err,
			}
			closed, failure = closeFailedAttempt(
				conversation,
				lifecycle,
				failure,
			)
			return nil
		}
		sources := append(
			[]int64{startEvent.Seq, summaryEvent.Seq},
			summarized.shadowedSeqs...,
		)
		_, err = session.AppendSurface(
			conversation,
			session.UserMessageAdded,
			summarized.checkpoint,
			session.SurfaceIntent{
				Operation:       session.SurfaceReplace(summarized.start, summarized.end),
				SourceEventSeqs: &sources,
			},
		)
		if err != nil {
			failure = &transactionFailure{
				stage: transactionCommit,
				err:   err,
			}
			closed, failure = closeFailedAttempt(
				conversation,
				lifecycle,
				failure,
			)
			return nil
		}
		endEvent, err := session.Append(
			conversation,
			compaction.EndEvent,
			compaction.End{
				CompactionID:    lifecycle.CompactionID,
				SourceCommandID: cloneString(lifecycle.SourceCommandID),
				Turn:            cloneInt64(lifecycle.Turn),
			},
		)
		if err != nil {
			failure = &transactionFailure{
				stage: transactionCommit,
				err:   err,
			}
			return err
		}
		closed = true
		outcome = compaction.Result{
			CompactionID:    lifecycle.CompactionID,
			SourceCommandID: cloneString(lifecycle.SourceCommandID),
			StartSeq:        startEvent.Seq,
			SummarySeq:      summaryEvent.Seq,
			EndSeq:          endEvent.Seq,
			Summary:         summarySnapshot,
			ShadowedRange: compaction.SurfaceRange{
				Start: summarized.start,
				End:   summarized.end,
			},
			ShadowedSeqs:       append([]int64(nil), summarized.shadowedSeqs...),
			ShadowedTokenCount: summarized.shadowedTokenCount,
		}
		return nil
	})
	if serializedErr != nil && failure == nil {
		failure = &transactionFailure{
			stage: transactionCommit,
			err:   serializedErr,
		}
	}
	return outcome, closed, failure
}

func closeFailedAttempt(
	conversation *session.Session,
	lifecycle compaction.Start,
	failure *transactionFailure,
) (bool, *transactionFailure) {
	detail := failure.err.Error()
	_, err := session.Append(
		conversation,
		compaction.EndEvent,
		compaction.End{
			CompactionID:    lifecycle.CompactionID,
			SourceCommandID: cloneString(lifecycle.SourceCommandID),
			Turn:            cloneInt64(lifecycle.Turn),
			Error:           &detail,
		},
	)
	if err == nil {
		return true, failure
	}
	return false, &transactionFailure{
		stage: transactionCommit,
		err:   err,
	}
}

func (coordinator *regionCompactor) failTransaction(
	requestContext context.Context,
	conversation *session.Session,
	lifecycle compaction.Start,
	settings transactionOptions,
	failure transactionFailure,
) (compaction.Result, error) {
	closed := false
	selectedFailure := &failure
	closeErr := session.SerializeProducer(conversation, func() error {
		closed, selectedFailure = closeFailedAttempt(
			conversation,
			lifecycle,
			selectedFailure,
		)
		return nil
	})
	if closeErr != nil {
		selectedFailure = &transactionFailure{
			stage: transactionCommit,
			err:   closeErr,
		}
	}
	if closed && settings.flush != nil {
		// The primary transaction failure wins over a durability-checkpoint
		// failure, matching the source transaction's failure precedence.
		_ = settings.flush(context.WithoutCancel(requestContext))
	}
	if settings.standalone {
		if cancelErr := contextFailure(requestContext); cancelErr != nil {
			return compaction.Result{}, manualCancellation(cancelErr)
		}
	}
	if selectedFailure != nil {
		return compaction.Result{}, classifyTransactionFailure(
			settings.standalone,
			selectedFailure,
		)
	}
	return compaction.Result{}, errors.New("compaction-basic: failed transaction lost its failure")
}

func (coordinator *regionCompactor) assertStable(
	requestContext context.Context,
	conversation *session.Session,
	prepared preparedCompaction,
	selectedOnly bool,
) error {
	if !selectedOnly {
		current, err := coordinator.meter.Measure(
			requestContext,
			conversation,
			nil,
		)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(current.Nodes, prepared.measurement.Nodes) {
			return &surfaceChangedError{
				message: "compaction: Session Surface changed during summarization",
			}
		}
		return nil
	}
	currentSelection, err := validateSurfaceRegion(
		conversation,
		prepared.start,
		prepared.end,
	)
	if err != nil {
		return &surfaceChangedError{
			message: "compaction: selected span is no longer a valid replacement target",
			cause:   err,
		}
	}
	if !reflect.DeepEqual(currentSelection.shadowedSeqs, prepared.shadowedSeqs) {
		return &surfaceChangedError{
			message: "compaction: selected span changed during summarization",
		}
	}
	current, err := coordinator.meter.Measure(
		requestContext,
		conversation,
		nil,
	)
	if err != nil {
		return err
	}
	if currentSelection.startIndex < 0 ||
		currentSelection.endIndex >= len(current.Nodes) {
		return &surfaceChangedError{
			message: "compaction: selected span was rewritten during summarization",
		}
	}
	measured := current.Nodes[currentSelection.startIndex : currentSelection.endIndex+1]
	if !reflect.DeepEqual(measured, prepared.selectedNodes) {
		return &surfaceChangedError{
			message: "compaction: selected span was rewritten during summarization",
		}
	}
	return nil
}

func assertNoActiveCompaction(
	conversation *session.Session,
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

func classifyTransactionFailure(
	standalone bool,
	failure *transactionFailure,
) error {
	if !standalone {
		return failure.err
	}
	if failure.stage == transactionCommit {
		return &compaction.ManualError{
			Code:    compaction.ManualErrorCommit,
			Message: "manual compaction did not commit cleanly",
			Cause:   failure.err,
		}
	}
	var changed *surfaceChangedError
	if errors.As(failure.err, &changed) {
		return &compaction.ManualError{
			Code:    compaction.ManualErrorChanged,
			Message: "the compacted history changed during manual compaction",
			Cause:   failure.err,
		}
	}
	return &compaction.ManualError{
		Code:    compaction.ManualErrorSummary,
		Message: "manual compaction could not produce a smaller summary",
		Cause:   failure.err,
	}
}

func manualCancellation(cause error) error {
	return &compaction.ManualError{
		Code:    compaction.ManualErrorCancelled,
		Message: "manual compaction was cancelled",
		Cause:   cause,
	}
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

func indexOfSequence(nodes []int64, sequence int64) int {
	for nodeIndex, candidate := range nodes {
		if candidate == sequence {
			return nodeIndex
		}
	}
	return -1
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
