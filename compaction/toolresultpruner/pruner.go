package toolresultpruner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/compaction"
	"github.com/gorenx/goren/llm/tokenmeter"
	"github.com/gorenx/goren/session"
)

// ToolResultPruner owns deterministic pruning policy and Session replacement
// orchestration. Plugin only owns its Runtime publication and dependency lifecycle.
type ToolResultPruner struct {
	settings ResolvedConfig
	meter    tokenmeter.Meter
}

func newToolResultPruner(settings ResolvedConfig) *ToolResultPruner {
	return &ToolResultPruner{
		settings: settings,
	}
}

func (implementation *ToolResultPruner) bind(meter tokenmeter.Meter) {
	implementation.meter = meter
}

func (implementation *ToolResultPruner) release() {
	implementation.meter = nil
}

// MeasureContent counts text blocks in Unicode code points.
func (*ToolResultPruner) MeasureContent(blocks []agentmessage.ContentBlock) (int, error) {
	detachedBlocks, err := agentmessage.CloneContentBlocks(blocks)
	if err != nil {
		return 0, err
	}
	totalChars := 0
	for blockIndex, blockValue := range detachedBlocks {
		textValue, textual, textErr := textFromBlock(blockValue)
		if textErr != nil {
			return 0, fmt.Errorf(
				"toolresultpruner: measure content block %d: %w",
				blockIndex,
				textErr,
			)
		}
		if !textual {
			continue
		}
		blockChars := utf8.RuneCountInString(textValue)
		if blockChars > int(^uint(0)>>1)-totalChars {
			return 0, errors.New("toolresultpruner: character count overflows int")
		}
		totalChars += blockChars
	}
	return totalChars, nil
}

// PruneContent retains head/marker/tail while preserving rich-block order.
func (implementation *ToolResultPruner) PruneContent(
	blocks []agentmessage.ContentBlock,
) ([]agentmessage.ContentBlock, bool, error) {
	totalChars, err := implementation.MeasureContent(blocks)
	if err != nil {
		return nil, false, err
	}
	if totalChars <= implementation.settings.ThresholdChars {
		return nil, false, nil
	}
	detachedBlocks, err := agentmessage.CloneContentBlocks(blocks)
	if err != nil {
		return nil, false, err
	}
	removedStart := implementation.settings.HeadChars
	removedEnd := totalChars - implementation.settings.TailChars
	prunedBlocks := make([]agentmessage.ContentBlock, 0, len(blocks)+1)
	consumedChars := 0
	markerInserted := false
	for blockIndex, blockValue := range detachedBlocks {
		textValue, textual, textErr := textFromBlock(blockValue)
		if textErr != nil {
			return nil, false, fmt.Errorf(
				"toolresultpruner: prune content block %d: %w",
				blockIndex,
				textErr,
			)
		}
		if !textual {
			prunedBlocks = append(prunedBlocks, blockValue)
			continue
		}
		points := []rune(textValue)
		blockStart := consumedChars
		blockEnd := blockStart + len(points)
		headEnd := boundedIndex(removedStart-blockStart, len(points))
		tailStart := boundedIndex(removedEnd-blockStart, len(points))
		intersectsRemoved := blockStart < removedEnd && blockEnd > removedStart
		insertMarker := intersectsRemoved && !markerInserted
		textBytes := make([]byte, 0, len(textValue)+len(PruneMarker))
		textBytes = append(textBytes, string(points[:headEnd])...)
		if insertMarker {
			textBytes = append(textBytes, PruneMarker...)
			markerInserted = true
		}
		textBytes = append(textBytes, string(points[tailStart:])...)
		if len(textBytes) != 0 {
			rewritten, rewriteErr := replaceTextBlock(blockValue, string(textBytes))
			if rewriteErr != nil {
				return nil, false, rewriteErr
			}
			prunedBlocks = append(prunedBlocks, rewritten)
		}
		consumedChars = blockEnd
	}
	if !markerInserted {
		return nil, false, errors.New(
			"toolresultpruner: failed to locate removed text span",
		)
	}
	charsAfter, err := implementation.MeasureContent(prunedBlocks)
	if err != nil {
		return nil, false, err
	}
	if charsAfter > implementation.settings.ThresholdChars || charsAfter >= totalChars {
		return nil, false, errors.New(
			"toolresultpruner: replacement must be smaller and within threshold",
		)
	}
	return prunedBlocks, true, nil
}

// PruneSession builds all shadow-price and replacement events from the Snapshot
// visible at the FIFO head and commits the complete batch atomically.
func (implementation *ToolResultPruner) PruneSession(
	requestContext context.Context,
	conversation session.Context,
) (Result, error) {
	if requestContext == nil {
		return Result{}, errors.New("toolresultpruner: Context is nil")
	}
	if conversation == nil {
		return Result{}, errors.New("toolresultpruner: Session is nil")
	}
	if implementation.meter == nil {
		return Result{}, errors.New("toolresultpruner: Token Meter is not bound")
	}
	if err := requestContext.Err(); err != nil {
		return Result{}, err
	}
	plan := &pruningPlan{
		pruner: implementation,
	}
	if _, err := conversation.Commit(requestContext, plan); err != nil {
		return Result{}, err
	}
	return plan.result, nil
}

type pruningPlan struct {
	pruner *ToolResultPruner
	result Result
}

func (plan *pruningPlan) Build(
	requestContext context.Context,
	currentSnapshot session.Snapshot,
) ([]session.EventDraft, error) {
	candidates := make([]session.Event, 0)
	for _, sequence := range currentSnapshot.Surface.Nodes {
		if sequence < 0 || sequence >= int64(len(currentSnapshot.Events)) ||
			currentSnapshot.Events[sequence].Seq != sequence {
			return nil, fmt.Errorf(
				"toolresultpruner: Surface seq %d has no matching Event",
				sequence,
			)
		}
		candidate := currentSnapshot.Events[sequence]
		if candidate.Type == session.ToolResultEventName {
			candidates = append(candidates, candidate)
		}
	}
	drafts := make([]session.EventDraft, 0, len(candidates)*2)
	outcome := Result{}
	nextSequence := currentSnapshot.Barrier.NextSeq
	for _, candidate := range candidates {
		if requestContext.Err() != nil {
			return nil, context.Cause(requestContext)
		}
		facts, err := decodeToolResultCandidate(candidate)
		if err != nil {
			return nil, err
		}
		blocks := facts.message.ContentValue()
		if len(blocks) != 1 {
			return nil, fmt.Errorf(
				"toolresultpruner: tool/result at seq %d has invalid content",
				candidate.Seq,
			)
		}
		blockValue, valid := blocks[0].(agentmessage.ToolResultBlock)
		if !valid {
			return nil, fmt.Errorf(
				"toolresultpruner: tool/result at seq %d has another block type",
				candidate.Seq,
			)
		}
		prunedContent, changed, err := plan.pruner.PruneContent(
			blockValue.Content,
		)
		if err != nil {
			return nil, err
		}
		if !changed {
			continue
		}
		charsBefore, err := plan.pruner.MeasureContent(blockValue.Content)
		if err != nil {
			return nil, err
		}
		charsAfter, err := plan.pruner.MeasureContent(prunedContent)
		if err != nil {
			return nil, err
		}
		shadowedTokens, err := plan.pruner.meter.EstimateMessage(facts.message)
		if err != nil {
			return nil, err
		}
		if shadowedTokens < 0 || shadowedTokens > 1<<53-1 {
			return nil, errors.New(
				"toolresultpruner: Token Meter returned an invalid shadow price",
			)
		}
		pruneDraft, err := session.NewEventDraft(
			compaction.PruneEvent,
			compaction.Prune{
				ShadowedRange: compaction.SurfaceRange{
					Start: candidate.Seq,
					End:   candidate.Seq,
				},
				ShadowedSeqs:       []int64{candidate.Seq},
				ShadowedTokenCount: shadowedTokens,
			},
		)
		if err != nil {
			return nil, err
		}
		replacementDraft, err := session.NewToolResultContentReplacementDraft(
			candidate,
			prunedContent,
		)
		if err != nil {
			return nil, err
		}
		drafts = append(drafts, pruneDraft, replacementDraft)
		outcome.Pruned = append(outcome.Pruned, Entry{
			OriginalSeq:    candidate.Seq,
			ReplacementSeq: nextSequence + 1,
			CallID:         blockValue.ToolCallID,
			CharsBefore:    charsBefore,
			CharsAfter:     charsAfter,
		})
		outcome.CharsRemoved += charsBefore - charsAfter
		nextSequence += 2
	}
	plan.result = outcome
	return drafts, nil
}

type toolResultFacts struct {
	message agentmessage.ToolResultMessage
}

func decodeToolResultCandidate(candidate session.Event) (toolResultFacts, error) {
	messageValue, err := session.DeriveEventMessage(candidate)
	if err != nil {
		return toolResultFacts{}, err
	}
	typedMessage, valid := messageValue.(agentmessage.ToolResultMessage)
	if !valid {
		return toolResultFacts{}, fmt.Errorf(
			"toolresultpruner: Event at seq %d is not a tool result",
			candidate.Seq,
		)
	}
	return toolResultFacts{
		message: typedMessage,
	}, nil
}

func textFromBlock(blockValue agentmessage.ContentBlock) (string, bool, error) {
	if blockValue == nil {
		return "", false, errors.New("content block is nil")
	}
	if blockValue.ContentType() != "text" {
		return "", false, nil
	}
	textual, valid := blockValue.(agentmessage.PlainTextContent)
	if !valid {
		return "", false, errors.New("text block does not expose plain text")
	}
	textValue, available := textual.PlainText()
	if !available {
		return "", false, errors.New("text block has invalid text")
	}
	return textValue, true, nil
}

func replaceTextBlock(blockValue agentmessage.ContentBlock, textValue string) (agentmessage.ContentBlock, error) {
	switch blockValue.(type) {
	case agentmessage.TextBlock, *agentmessage.TextBlock:
		return agentmessage.NewTextBlock(textValue), nil
	}
	encoded, err := json.Marshal(blockValue)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		return nil, err
	}
	rewrittenText, err := json.Marshal(textValue)
	if err != nil {
		return nil, err
	}
	fields["text"] = rewrittenText
	rewritten, err := json.Marshal([]map[string]json.RawMessage{fields})
	if err != nil {
		return nil, err
	}
	blocks, err := agentmessage.DecodeContentBlocks(rewritten)
	if err != nil {
		return nil, err
	}
	if len(blocks) != 1 {
		return nil, errors.New("toolresultpruner: rewritten text block is absent")
	}
	return blocks[0], nil
}

func boundedIndex(value int, length int) int {
	if value < 0 {
		return 0
	}
	if value > length {
		return length
	}
	return value
}

var _ Pruner = (*ToolResultPruner)(nil)
