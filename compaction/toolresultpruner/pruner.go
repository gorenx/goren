package toolresultpruner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/gorenx/goren/compaction"
	"github.com/gorenx/goren/llm"
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
func (*ToolResultPruner) MeasureContent(blocks []llm.ContentBlock) (int, error) {
	detachedBlocks, err := llm.CloneContentBlocks(blocks)
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
	blocks []llm.ContentBlock,
) ([]llm.ContentBlock, bool, error) {
	totalChars, err := implementation.MeasureContent(blocks)
	if err != nil {
		return nil, false, err
	}
	if totalChars <= implementation.settings.ThresholdChars {
		return nil, false, nil
	}
	detachedBlocks, err := llm.CloneContentBlocks(blocks)
	if err != nil {
		return nil, false, err
	}
	removedStart := implementation.settings.HeadChars
	removedEnd := totalChars - implementation.settings.TailChars
	prunedBlocks := make([]llm.ContentBlock, 0, len(blocks)+1)
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

// PruneSession appends each shadow price and replacement in one serialized
// synchronous pass. Replacements committed before a later error remain valid.
func (implementation *ToolResultPruner) PruneSession(
	requestContext context.Context,
	conversation *session.Session,
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
	outcome := Result{}
	err := session.SerializeProducer(
		conversation,
		func() error {
			entries, surface := conversation.ReadCut()
			candidates := make([]session.Event, 0)
			for _, sequence := range surface.Nodes {
				if sequence < 0 || sequence >= int64(len(entries)) ||
					entries[sequence].Seq != sequence {
					return fmt.Errorf(
						"toolresultpruner: Surface seq %d has no matching Event",
						sequence,
					)
				}
				candidate := entries[sequence]
				if candidate.Type == session.ToolResultEventName {
					candidates = append(candidates, candidate)
				}
			}
			for _, candidate := range candidates {
				if contextErr := requestContext.Err(); contextErr != nil {
					return contextErr
				}
				facts, factsErr := decodeToolResultCandidate(candidate)
				if factsErr != nil {
					return factsErr
				}
				blocks := facts.message.ContentValue()
				if len(blocks) != 1 {
					return fmt.Errorf(
						"toolresultpruner: tool/result at seq %d has invalid content",
						candidate.Seq,
					)
				}
				blockValue, valid := blocks[0].(llm.ToolResultBlock)
				if !valid {
					return fmt.Errorf(
						"toolresultpruner: tool/result at seq %d has another block type",
						candidate.Seq,
					)
				}
				prunedContent, changed, pruneErr := implementation.PruneContent(
					blockValue.Content,
				)
				if pruneErr != nil {
					return pruneErr
				}
				if !changed {
					continue
				}
				charsBefore, measureErr := implementation.MeasureContent(blockValue.Content)
				if measureErr != nil {
					return measureErr
				}
				charsAfter, measureErr := implementation.MeasureContent(prunedContent)
				if measureErr != nil {
					return measureErr
				}
				shadowedTokens, priceErr := implementation.meter.EstimateMessage(facts.message)
				if priceErr != nil {
					return priceErr
				}
				if shadowedTokens < 0 || shadowedTokens > 1<<53-1 {
					return errors.New(
						"toolresultpruner: Token Meter returned an invalid shadow price",
					)
				}
				if _, appendErr := session.Append(
					conversation,
					compaction.PruneEvent,
					compaction.Prune{
						ShadowedRange: compaction.SurfaceRange{
							Start: candidate.Seq,
							End:   candidate.Seq,
						},
						ShadowedSeqs:       []int64{candidate.Seq},
						ShadowedTokenCount: shadowedTokens,
					},
				); appendErr != nil {
					return appendErr
				}
				replacement, appendErr := session.AppendToolResultContentReplacement(
					conversation,
					candidate.Seq,
					prunedContent,
				)
				if appendErr != nil {
					return appendErr
				}
				outcome.Pruned = append(outcome.Pruned, Entry{
					OriginalSeq:    candidate.Seq,
					ReplacementSeq: replacement.Seq,
					CallID:         blockValue.ToolCallID,
					CharsBefore:    charsBefore,
					CharsAfter:     charsAfter,
				})
				outcome.CharsRemoved += charsBefore - charsAfter
			}
			return nil
		},
	)
	return outcome, err
}

type toolResultFacts struct {
	message llm.ToolResultMessage
}

func decodeToolResultCandidate(candidate session.Event) (toolResultFacts, error) {
	messageValue, err := session.DeriveEventMessage(candidate)
	if err != nil {
		return toolResultFacts{}, err
	}
	typedMessage, valid := messageValue.(llm.ToolResultMessage)
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

func textFromBlock(blockValue llm.ContentBlock) (string, bool, error) {
	if blockValue == nil {
		return "", false, errors.New("content block is nil")
	}
	if blockValue.ContentType() != "text" {
		return "", false, nil
	}
	textual, valid := blockValue.(llm.PlainTextContent)
	if !valid {
		return "", false, errors.New("text block does not expose plain text")
	}
	textValue, available := textual.PlainText()
	if !available {
		return "", false, errors.New("text block has invalid text")
	}
	return textValue, true, nil
}

func replaceTextBlock(blockValue llm.ContentBlock, textValue string) (llm.ContentBlock, error) {
	switch blockValue.(type) {
	case llm.TextBlock, *llm.TextBlock:
		return llm.NewTextBlock(textValue), nil
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
	blocks, err := llm.DecodeContentBlocks(rewritten)
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
