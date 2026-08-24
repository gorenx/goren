package basic

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

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

type startPlan struct {
	requested       surfaceSelection
	compactionID    compaction.ID
	standalone      bool
	sourceCommandID *string
	selected        surfaceSelection
	startEvent      session.Event
	lifecycle       compaction.Start
}

func (plan *startPlan) Build(
	_ context.Context,
	snapshot session.Snapshot,
) ([]session.EventDraft, error) {
	selected, err := validateSurfaceRegionSnapshot(
		snapshot,
		plan.requested.start,
		plan.requested.end,
	)
	if err != nil {
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
	var turnOwner *int64
	if plan.standalone {
		if logState.OpenTurn != nil {
			return nil, &compaction.ManualError{
				Code:    compaction.ManualErrorBusy,
				Message: "manual compaction: the Session already has an open turn",
			}
		}
	} else {
		if logState.OpenTurn == nil {
			return nil, errors.New(
				"compactRegion: no open turn; automatic compaction events must be enclosed in a turn",
			)
		}
		ownerValue := *logState.OpenTurn
		turnOwner = &ownerValue
	}
	lifecycle := compaction.Start{
		CompactionID:    plan.compactionID,
		SourceCommandID: cloneString(plan.sourceCommandID),
		Turn:            cloneInt64(turnOwner),
	}
	draft, err := session.NewEventDraft(compaction.StartEvent, lifecycle)
	if err != nil {
		return nil, err
	}
	plan.selected = selected
	plan.startEvent = session.Event{
		Seq: snapshot.Barrier.NextSeq,
	}
	plan.lifecycle = lifecycle
	return []session.EventDraft{draft}, nil
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
	conversation session.Context,
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
	snapshot := conversation.Snapshot()
	surface := snapshot.Surface
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
	plan := &startPlan{
		requested:       requested,
		compactionID:    compactionID,
		standalone:      settings.standalone,
		sourceCommandID: cloneString(settings.sourceCommandID),
	}
	_, err = ownerContext.Session.Commit(requestContext, plan)
	if err != nil {
		return compaction.Result{}, err
	}
	selected := plan.selected
	startEvent := plan.startEvent
	lifecycle := plan.lifecycle

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
	conversation session.Context,
	start int64,
	end int64,
) (surfaceSelection, error) {
	if conversation == nil {
		return surfaceSelection{}, errors.New("compactRegion: Session is nil")
	}
	return validateSurfaceRegionSnapshot(conversation.Snapshot(), start, end)
}

func validateSurfaceRegionSnapshot(
	snapshot session.Snapshot,
	start int64,
	end int64,
) (surfaceSelection, error) {
	surface := snapshot.Surface
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
	balancedBefore, err := compaction.ToolPairingBalancedBeforeSnapshot(
		snapshot,
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
	balancedAfter, err := compaction.ToolPairingBalancedAfterSnapshot(
		snapshot,
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
	conversation session.Context,
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
	conversation session.Context,
	startEvent session.Event,
	lifecycle compaction.Start,
	settings transactionOptions,
	summarized summarizedCompaction,
) (compaction.Result, bool, *transactionFailure) {
	plan := &completionPlan{
		operationContext: requestContext,
		startEvent:       startEvent,
		lifecycle:        lifecycle,
		selectedStable:   settings.selectedStable,
		summarized:       summarized,
	}
	_, executeErr := conversation.Commit(
		context.WithoutCancel(requestContext),
		plan,
	)
	if executeErr != nil {
		failure := plan.failure
		if failure == nil {
			failure = &transactionFailure{
				stage: transactionCommit,
				err:   executeErr,
			}
		}
		return compaction.Result{}, false, failure
	}
	return plan.result, true, plan.failure
}

type completionPlan struct {
	operationContext context.Context
	startEvent       session.Event
	lifecycle        compaction.Start
	selectedStable   bool
	summarized       summarizedCompaction
	result           compaction.Result
	failure          *transactionFailure
}

func (plan *completionPlan) Build(
	_ context.Context,
	snapshot session.Snapshot,
) ([]session.EventDraft, error) {
	if err := contextFailure(plan.operationContext); err != nil {
		plan.failure = &transactionFailure{
			stage: transactionSummary,
			err:   err,
		}
		return plan.buildFailure()
	}
	if err := assertStableSnapshot(
		snapshot,
		plan.summarized.preparedCompaction,
		plan.selectedStable,
	); err != nil {
		plan.failure = &transactionFailure{
			stage: transactionSummary,
			err:   err,
		}
		return plan.buildFailure()
	}
	summarySnapshot, err := llm.CloneContentBlocks(
		plan.summarized.generated.summary,
	)
	if err != nil {
		plan.failure = &transactionFailure{
			stage: transactionCommit,
			err:   err,
		}
		return plan.buildFailure()
	}
	summaryDraft, err := session.NewEventDraft(
		compaction.SummaryEvent,
		compaction.Summary{
			CompactionID:    plan.lifecycle.CompactionID,
			SourceCommandID: cloneString(plan.lifecycle.SourceCommandID),
			Summary:         summarySnapshot,
			RawOutput:       plan.summarized.generated.rawOutput,
			LLMStreamCall:   plan.summarized.generated.llmStreamCall,
			ShadowedRange: compaction.SurfaceRange{
				Start: plan.summarized.start,
				End:   plan.summarized.end,
			},
			ShadowedSeqs: append(
				[]int64(nil),
				plan.summarized.shadowedSeqs...,
			),
			ShadowedTokenCount: plan.summarized.shadowedTokenCount,
			Provider:           plan.summarized.generated.provider,
			Model:              plan.summarized.generated.model,
			MaxTokens:          cloneInt(plan.summarized.generated.maxTokens),
			Usage:              cloneUsage(plan.summarized.generated.usage),
		},
	)
	if err != nil {
		plan.failure = &transactionFailure{
			stage: transactionCommit,
			err:   err,
		}
		return plan.buildFailure()
	}
	summarySequence := snapshot.Barrier.NextSeq
	sources := append(
		[]int64{plan.startEvent.Seq, summarySequence},
		plan.summarized.shadowedSeqs...,
	)
	replacementDraft, err := session.NewSurfaceEventDraft(
		session.UserMessageAdded,
		plan.summarized.checkpoint,
		session.SurfaceIntent{
			Operation: session.SurfaceReplace(
				plan.summarized.start,
				plan.summarized.end,
			),
			SourceEventSeqs: &sources,
		},
	)
	if err != nil {
		plan.failure = &transactionFailure{
			stage: transactionCommit,
			err:   err,
		}
		return plan.buildFailure()
	}
	endDraft, err := session.NewEventDraft(
		compaction.EndEvent,
		compaction.End{
			CompactionID:    plan.lifecycle.CompactionID,
			SourceCommandID: cloneString(plan.lifecycle.SourceCommandID),
			Turn:            cloneInt64(plan.lifecycle.Turn),
		},
	)
	if err != nil {
		plan.failure = &transactionFailure{
			stage: transactionCommit,
			err:   err,
		}
		return plan.buildFailure()
	}
	plan.result = compaction.Result{
		CompactionID:    plan.lifecycle.CompactionID,
		SourceCommandID: cloneString(plan.lifecycle.SourceCommandID),
		StartSeq:        plan.startEvent.Seq,
		SummarySeq:      summarySequence,
		EndSeq:          summarySequence + 2,
		Summary:         summarySnapshot,
		ShadowedRange: compaction.SurfaceRange{
			Start: plan.summarized.start,
			End:   plan.summarized.end,
		},
		ShadowedSeqs: append(
			[]int64(nil),
			plan.summarized.shadowedSeqs...,
		),
		ShadowedTokenCount: plan.summarized.shadowedTokenCount,
	}
	return []session.EventDraft{
		summaryDraft,
		replacementDraft,
		endDraft,
	}, nil
}

func (plan *completionPlan) buildFailure() ([]session.EventDraft, error) {
	detail := plan.failure.err.Error()
	draft, err := session.NewEventDraft(
		compaction.EndEvent,
		compaction.End{
			CompactionID:    plan.lifecycle.CompactionID,
			SourceCommandID: cloneString(plan.lifecycle.SourceCommandID),
			Turn:            cloneInt64(plan.lifecycle.Turn),
			Error:           &detail,
		},
	)
	if err != nil {
		plan.failure = &transactionFailure{
			stage: transactionCommit,
			err:   err,
		}
		return nil, err
	}
	return []session.EventDraft{draft}, nil
}

func (coordinator *regionCompactor) failTransaction(
	requestContext context.Context,
	conversation session.Context,
	lifecycle compaction.Start,
	settings transactionOptions,
	failure transactionFailure,
) (compaction.Result, error) {
	detail := failure.err.Error()
	draft, closeErr := session.NewEventDraft(
		compaction.EndEvent,
		compaction.End{
			CompactionID:    lifecycle.CompactionID,
			SourceCommandID: cloneString(lifecycle.SourceCommandID),
			Turn:            cloneInt64(lifecycle.Turn),
			Error:           &detail,
		},
	)
	closed := false
	if closeErr == nil {
		_, closeErr = conversation.Commit(
			context.WithoutCancel(requestContext),
			session.Batch(draft),
		)
		closed = closeErr == nil
	}
	selectedFailure := &failure
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

func assertStableSnapshot(
	snapshot session.Snapshot,
	prepared preparedCompaction,
	selectedOnly bool,
) error {
	if !selectedOnly {
		if len(snapshot.Surface.Nodes) != len(prepared.measurement.Nodes) {
			return &surfaceChangedError{
				message: "compaction: Session Surface changed during summarization",
			}
		}
		for index, sequence := range snapshot.Surface.Nodes {
			if sequence != prepared.measurement.Nodes[index].Seq {
				return &surfaceChangedError{
					message: "compaction: Session Surface changed during summarization",
				}
			}
		}
		return nil
	}
	currentSelection, err := validateSurfaceRegionSnapshot(
		snapshot,
		prepared.start,
		prepared.end,
	)
	if err != nil {
		return &surfaceChangedError{
			message: "compaction: selected span is no longer a valid replacement target",
			cause:   err,
		}
	}
	if !equalSequences(currentSelection.shadowedSeqs, prepared.shadowedSeqs) {
		return &surfaceChangedError{
			message: "compaction: selected span changed during summarization",
		}
	}
	if currentSelection.startIndex < 0 ||
		currentSelection.endIndex >= len(snapshot.Surface.Nodes) {
		return &surfaceChangedError{
			message: "compaction: selected span was rewritten during summarization",
		}
	}
	measured := snapshot.Surface.Nodes[currentSelection.startIndex : currentSelection.endIndex+1]
	if len(measured) != len(prepared.selectedNodes) {
		return &surfaceChangedError{
			message: "compaction: selected span was rewritten during summarization",
		}
	}
	for index, sequence := range measured {
		if sequence != prepared.selectedNodes[index].Seq {
			return &surfaceChangedError{
				message: "compaction: selected span was rewritten during summarization",
			}
		}
	}
	return nil
}

func equalSequences(first []int64, second []int64) bool {
	if len(first) != len(second) {
		return false
	}
	for index, sequence := range first {
		if sequence != second[index] {
			return false
		}
	}
	return true
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
