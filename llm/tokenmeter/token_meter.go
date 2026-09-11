package tokenmeter

import (
	"context"
	"errors"
	"fmt"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

// TokenMeter incrementally measures one append-only log. Its owner serializes access
// and must not reuse it for another Session creation. The zero value is ready to use.
type TokenMeter struct {
	fold replayState
}

type replayStep struct {
	turn          int64
	step          int64
	surfaceTokens int64
}

type measurementAnchor struct {
	header        *session.EpochHeader
	surfaceTokens int64
	anchorValue   Baseline
}

type replayState struct {
	consumedEvents int64
	header         *session.EpochHeader
	nodes          []SurfaceNode
	surfaceTokens  int64
	stepStart      *replayStep
	anchor         *measurementAnchor
}

// Measure will fold durable request and Surface facts into one measurement.
func (owner *TokenMeter) Measure(
	requestContext context.Context,
	entries []session.Event,
	requestHeader *session.EpochHeader,
) (Measurement, error) {
	if requestContext == nil {
		return Measurement{}, errors.New("tokenmeter: Measure Context is nil")
	}
	if err := requestContext.Err(); err != nil {
		return Measurement{}, err
	}
	candidate := owner.fold
	candidate.header = cloneHeader(owner.fold.header)
	candidate.nodes = cloneNodes(owner.fold.nodes)
	candidate.stepStart = cloneStep(owner.fold.stepStart)
	candidate.anchor = cloneAnchor(owner.fold.anchor)
	if int64(len(entries)) < candidate.consumedEvents {
		candidate = replayState{}
	}
	if err := owner.replay(requestContext, &candidate, entries); err != nil {
		return Measurement{}, err
	}
	fold := &candidate
	headerValue := cloneHeader(fold.header)
	if requestHeader != nil {
		canonical := session.CanonicalEpochHeader(*requestHeader)
		headerValue = &canonical
	}

	anchorValue := Baseline{
		Kind:   BaselineNone,
		Tokens: 0,
	}
	surfaceDelta := int64(0)
	if fold.anchor != nil && optionalHeaderEqual(fold.anchor.header, headerValue) {
		anchorValue = cloneBaseline(fold.anchor.anchorValue)
		surfaceDelta = fold.surfaceTokens - fold.anchor.surfaceTokens
	} else if headerValue != nil || fold.surfaceTokens != 0 {
		headerTokens, err := estimateHeader(headerValue)
		if err != nil {
			return Measurement{}, err
		}
		estimatedTokens, err := addTokens(headerTokens, fold.surfaceTokens)
		if err != nil {
			return Measurement{}, err
		}
		anchorValue = Baseline{
			Kind:   BaselineEstimated,
			Tokens: estimatedTokens,
		}
	}
	totalTokens, err := applySignedPressure(anchorValue.Tokens, surfaceDelta)
	if err != nil {
		return Measurement{}, err
	}
	if candidate.consumedEvents >= owner.fold.consumedEvents {
		owner.fold = candidate
	}
	return Measurement{
		LogRevision:        fold.consumedEvents,
		Baseline:           anchorValue,
		SurfaceDeltaTokens: surfaceDelta,
		TotalTokens:        totalTokens,
		SurfaceTokens:      fold.surfaceTokens,
		Nodes:              cloneNodes(fold.nodes),
	}, nil
}

// EstimateMessage will use the same fixed estimator as Measure.
func (*TokenMeter) EstimateMessage(messageValue agentmessage.Message) (int64, error) {
	return estimateMessage(messageValue)
}

func (owner *TokenMeter) replay(
	requestContext context.Context,
	fold *replayState,
	entries []session.Event,
) error {
	if fold.consumedEvents < 0 || fold.consumedEvents > int64(len(entries)) {
		return errors.New("tokenmeter: replay revision is outside the Session log")
	}
	for fold.consumedEvents < int64(len(entries)) {
		if err := requestContext.Err(); err != nil {
			return err
		}
		entry := entries[fold.consumedEvents]
		if entry.Seq != fold.consumedEvents {
			return fmt.Errorf(
				"tokenmeter: event seq %d does not match replay revision %d",
				entry.Seq,
				fold.consumedEvents,
			)
		}
		if err := owner.foldEntry(fold, entry, entries); err != nil {
			return err
		}
		fold.consumedEvents++
	}
	return nil
}

func (*TokenMeter) foldEntry(
	fold *replayState,
	entry session.Event,
	entries []session.Event,
) error {
	nextHeader := cloneHeader(fold.header)
	nextStep := cloneStep(fold.stepStart)
	nextAnchor := cloneAnchor(fold.anchor)

	switch entry.Type {
	case session.RequestHeaderEventName:
		var snapshot session.RequestHeaderSnapshot
		if err := decodePayload(entry, &snapshot); err != nil {
			return err
		}
		canonical := session.CanonicalEpochHeader(snapshot.Header)
		nextHeader = &canonical
	case session.StepStartEventName:
		if fold.stepStart != nil {
			return fmt.Errorf(
				"token meter: step/start at seq %d arrived before turn %d/step %d ended",
				entry.Seq,
				fold.stepStart.turn,
				fold.stepStart.step,
			)
		}
		var position session.StepPosition
		if err := decodePayload(entry, &position); err != nil {
			return err
		}
		nextStep = &replayStep{
			turn:          position.Turn,
			step:          position.Step,
			surfaceTokens: fold.surfaceTokens,
		}
	case session.StepEndEventName:
		var position session.StepPosition
		if err := decodePayload(entry, &position); err != nil {
			return err
		}
		if fold.stepStart == nil || fold.stepStart.turn != position.Turn ||
			fold.stepStart.step != position.Step {
			return fmt.Errorf(
				"token meter: step/end at seq %d has no matching step/start event",
				entry.Seq,
			)
		}
		nextStep = nil
	}

	var surfaceResult *surfaceFold
	if entry.SurfaceOp != nil {
		computed, err := foldSurface(fold.nodes, entry)
		if err != nil {
			return err
		}
		surfaceResult = &computed
	}

	if entry.Type == session.AssistantMessageEventName {
		facts, err := decodeAssistantMessageFacts(entry)
		if err != nil {
			return err
		}
		if fold.stepStart == nil || fold.stepStart.turn != facts.turn ||
			fold.stepStart.step != facts.step {
			return fmt.Errorf(
				"token meter: assistant/message at seq %d has no matching step/start event",
				entry.Seq,
			)
		}
		if surfaceResult == nil {
			return fmt.Errorf(
				"token meter: assistant/message at seq %d is not on the surface",
				entry.Seq,
			)
		}
		anchorSurfaceTokens, err := addTokens(
			fold.stepStart.surfaceTokens,
			surfaceResult.tokens,
		)
		if err != nil {
			return err
		}
		anchorValue := Baseline{}
		if facts.usage != nil && nextHeader != nil {
			providerAssistantTokens, estimateErr := estimateProviderAssistant(
				entries,
				entry,
				facts,
				surfaceResult.tokens,
			)
			if estimateErr != nil {
				return estimateErr
			}
			anchorSurfaceTokens, err = addTokens(
				fold.stepStart.surfaceTokens,
				providerAssistantTokens,
			)
			if err != nil {
				return err
			}
			providerTokens, usageErr := totalUsageTokens(*facts.usage)
			if usageErr != nil {
				return usageErr
			}
			headerTokens, headerErr := estimateHeader(nextHeader)
			if headerErr != nil {
				return headerErr
			}
			estimatedAnchor, anchorErr := addTokens(headerTokens, anchorSurfaceTokens)
			if anchorErr != nil {
				return anchorErr
			}
			if providerTokens >= estimatedAnchor {
				anchorValue = Baseline{
					Kind:   BaselineUsage,
					Tokens: providerTokens,
					Usage:  cloneUsage(facts.usage),
				}
			} else {
				anchorValue = Baseline{
					Kind:   BaselineEstimated,
					Tokens: estimatedAnchor,
				}
			}
		} else {
			headerTokens, headerErr := estimateHeader(nextHeader)
			if headerErr != nil {
				return headerErr
			}
			estimatedAnchor, anchorErr := addTokens(headerTokens, anchorSurfaceTokens)
			if anchorErr != nil {
				return anchorErr
			}
			anchorValue = Baseline{
				Kind:   BaselineEstimated,
				Tokens: estimatedAnchor,
			}
		}
		nextAnchor = &measurementAnchor{
			header:        cloneHeader(nextHeader),
			surfaceTokens: anchorSurfaceTokens,
			anchorValue:   anchorValue,
		}
	}

	fold.header = nextHeader
	fold.stepStart = nextStep
	if surfaceResult != nil {
		nextSurfaceTokens, err := applySurfaceDelta(fold.surfaceTokens, surfaceResult.delta)
		if err != nil {
			return err
		}
		fold.nodes = surfaceResult.nodes
		fold.surfaceTokens = nextSurfaceTokens
	}
	fold.anchor = nextAnchor
	return nil
}

func estimateProviderAssistant(
	entries []session.Event,
	assistantEntry session.Event,
	facts assistantMessageFacts,
	durableTokens int64,
) (int64, error) {
	if assistantEntry.SourceEventSeqs == nil {
		return durableTokens, nil
	}
	assembly := llm.NewBlockAssembler()
	seen := make(map[int64]struct{}, len(*assistantEntry.SourceEventSeqs))
	for _, sourceSeq := range *assistantEntry.SourceEventSeqs {
		if sourceSeq < 0 || sourceSeq >= assistantEntry.Seq || sourceSeq >= int64(len(entries)) {
			return 0, fmt.Errorf(
				"token meter: assistant/message at seq %d source seq %d is not earlier",
				assistantEntry.Seq,
				sourceSeq,
			)
		}
		if _, duplicate := seen[sourceSeq]; duplicate {
			return 0, fmt.Errorf(
				"token meter: assistant/message at seq %d repeats source seq %d",
				assistantEntry.Seq,
				sourceSeq,
			)
		}
		seen[sourceSeq] = struct{}{}
		sourceEntry := entries[sourceSeq]
		if sourceEntry.Type != session.AssistantChunkEventName {
			return 0, fmt.Errorf(
				"token meter: assistant/message at seq %d source seq %d is not assistant/chunk",
				assistantEntry.Seq,
				sourceSeq,
			)
		}
		chunkFacts, err := decodeAssistantChunkFacts(sourceEntry)
		if err != nil {
			return 0, err
		}
		if chunkFacts.turn != facts.turn || chunkFacts.step != facts.step {
			return 0, fmt.Errorf(
				"token meter: assistant/message at seq %d source seq %d belongs to another step",
				assistantEntry.Seq,
				sourceSeq,
			)
		}
		if err := assembly.Push(chunkFacts.chunk); err != nil {
			return 0, err
		}
	}
	blocks, err := assembly.AssembledBlocks()
	if err != nil {
		return 0, err
	}
	if len(blocks) == 0 {
		return 0, nil
	}
	contentTokens, err := estimateContent(blocks)
	if err != nil {
		return 0, err
	}
	return addTokens(contentTokens, roleOverhead)
}

func totalUsageTokens(usageValue llm.TokenUsage) (int64, error) {
	if err := validateUsage(usageValue); err != nil {
		return 0, err
	}
	total := usageValue.InputTokens
	values := []*int64{
		usageValue.CacheReadTokens,
		usageValue.CacheWriteTokens,
		&usageValue.OutputTokens,
	}
	var err error
	for _, value := range values {
		if value != nil {
			total, err = addTokens(total, *value)
			if err != nil {
				return 0, err
			}
		}
	}
	return total, nil
}

func optionalHeaderEqual(left *session.EpochHeader, right *session.EpochHeader) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return session.EpochHeaderEqual(*left, *right)
}

func cloneHeader(source *session.EpochHeader) *session.EpochHeader {
	if source == nil {
		return nil
	}
	detached := session.CanonicalEpochHeader(*source)
	return &detached
}

func cloneStep(source *replayStep) *replayStep {
	if source == nil {
		return nil
	}
	detached := *source
	return &detached
}

func cloneBaseline(source Baseline) Baseline {
	detached := source
	detached.Usage = cloneUsage(source.Usage)
	return detached
}

func cloneAnchor(source *measurementAnchor) *measurementAnchor {
	if source == nil {
		return nil
	}
	return &measurementAnchor{
		header:        cloneHeader(source.header),
		surfaceTokens: source.surfaceTokens,
		anchorValue:   cloneBaseline(source.anchorValue),
	}
}

func cloneNodes(source []SurfaceNode) []SurfaceNode {
	return append([]SurfaceNode(nil), source...)
}

var _ Meter = (*TokenMeter)(nil)
