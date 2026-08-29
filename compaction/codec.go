package compaction

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

const maxSafeInteger = int64(1<<53 - 1)

// DecodeStart strictly restores one compaction/start payload.
func DecodeStart(rawValue json.RawMessage) (Start, error) {
	var wireValue struct {
		CompactionID    ID              `json:"compactionId"`
		SourceCommandID json.RawMessage `json:"sourceCommandId"`
		Turn            json.RawMessage `json:"turn"`
	}
	if err := decodeStrictCompactionJSON(rawValue, &wireValue); err != nil {
		return Start{}, fmt.Errorf("compaction: decode start: %w", err)
	}
	if wireValue.CompactionID == "" {
		return Start{}, errors.New("compaction: start compactionId must be non-empty")
	}
	sourceCommandID, err := decodeOptionalIdentifier(
		wireValue.SourceCommandID,
		"start sourceCommandId",
	)
	if err != nil {
		return Start{}, err
	}
	turnValue, err := decodeRequiredNullableTurn(wireValue.Turn, "start turn")
	if err != nil {
		return Start{}, err
	}
	return Start{
		CompactionID:    wireValue.CompactionID,
		SourceCommandID: sourceCommandID,
		Turn:            turnValue,
	}, nil
}

// DecodeSummary strictly restores one compaction/summary payload and its
// merge-extended llmStreamCall/rawOutput union.
func DecodeSummary(rawValue json.RawMessage) (Summary, error) {
	var wireValue struct {
		CompactionID       ID              `json:"compactionId"`
		SourceCommandID    json.RawMessage `json:"sourceCommandId"`
		Summary            json.RawMessage `json:"summary"`
		RawOutput          json.RawMessage `json:"rawOutput"`
		LLMStreamCall      json.RawMessage `json:"llmStreamCall"`
		ShadowedRange      json.RawMessage `json:"shadowedRange"`
		ShadowedSeqs       json.RawMessage `json:"shadowedSeqs"`
		ShadowedTokenCount json.RawMessage `json:"shadowedTokenCount"`
		Provider           json.RawMessage `json:"provider"`
		Model              json.RawMessage `json:"model"`
		MaxTokens          json.RawMessage `json:"maxTokens"`
		Usage              json.RawMessage `json:"usage"`
	}
	if err := decodeStrictCompactionJSON(rawValue, &wireValue); err != nil {
		return Summary{}, fmt.Errorf("compaction: decode summary: %w", err)
	}
	if wireValue.CompactionID == "" {
		return Summary{}, errors.New("compaction: summary compactionId must be non-empty")
	}
	sourceCommandID, err := decodeOptionalIdentifier(
		wireValue.SourceCommandID,
		"summary sourceCommandId",
	)
	if err != nil {
		return Summary{}, err
	}
	summaryBlocks, err := decodeRequiredContent(wireValue.Summary, "summary")
	if err != nil {
		return Summary{}, err
	}
	rawOutput, rawOutputPresent, err := decodeOptionalContent(
		wireValue.RawOutput,
		"summary rawOutput",
	)
	if err != nil {
		return Summary{}, err
	}
	llmStreamCall, llmStreamCallPresent, err := decodeOptionalTrue(
		wireValue.LLMStreamCall,
		"summary llmStreamCall",
	)
	if err != nil {
		return Summary{}, err
	}
	if llmStreamCallPresent && !rawOutputPresent {
		return Summary{}, errors.New(
			"compaction: summary llmStreamCall requires rawOutput",
		)
	}
	shadowedRange, err := decodeRequiredRange(wireValue.ShadowedRange)
	if err != nil {
		return Summary{}, err
	}
	shadowedSeqs, err := decodeRequiredSequences(wireValue.ShadowedSeqs)
	if err != nil {
		return Summary{}, err
	}
	shadowedTokenCount, err := decodeRequiredSafeInteger(
		wireValue.ShadowedTokenCount,
		"summary shadowedTokenCount",
	)
	if err != nil {
		return Summary{}, err
	}
	providerValue, err := decodeRequiredString(wireValue.Provider, "summary provider")
	if err != nil {
		return Summary{}, err
	}
	modelValue, err := decodeRequiredString(wireValue.Model, "summary model")
	if err != nil {
		return Summary{}, err
	}
	maxTokens, err := decodeOptionalPositiveInt(wireValue.MaxTokens, "summary maxTokens")
	if err != nil {
		return Summary{}, err
	}
	usageValue, err := decodeOptionalUsage(wireValue.Usage)
	if err != nil {
		return Summary{}, err
	}
	return Summary{
		CompactionID:       wireValue.CompactionID,
		SourceCommandID:    sourceCommandID,
		Summary:            summaryBlocks,
		RawOutput:          rawOutput,
		LLMStreamCall:      llmStreamCall,
		ShadowedRange:      shadowedRange,
		ShadowedSeqs:       shadowedSeqs,
		ShadowedTokenCount: shadowedTokenCount,
		Provider:           providerValue,
		Model:              modelValue,
		MaxTokens:          maxTokens,
		Usage:              usageValue,
	}, nil
}

// DecodeEnd strictly restores one compaction/end payload.
func DecodeEnd(rawValue json.RawMessage) (End, error) {
	var wireValue struct {
		CompactionID    ID              `json:"compactionId"`
		SourceCommandID json.RawMessage `json:"sourceCommandId"`
		Turn            json.RawMessage `json:"turn"`
		Error           json.RawMessage `json:"error"`
	}
	if err := decodeStrictCompactionJSON(rawValue, &wireValue); err != nil {
		return End{}, fmt.Errorf("compaction: decode end: %w", err)
	}
	if wireValue.CompactionID == "" {
		return End{}, errors.New("compaction: end compactionId must be non-empty")
	}
	sourceCommandID, err := decodeOptionalIdentifier(
		wireValue.SourceCommandID,
		"end sourceCommandId",
	)
	if err != nil {
		return End{}, err
	}
	turnValue, err := decodeRequiredNullableTurn(wireValue.Turn, "end turn")
	if err != nil {
		return End{}, err
	}
	errorValue, err := decodeOptionalString(wireValue.Error, "end error", true)
	if err != nil {
		return End{}, err
	}
	return End{
		CompactionID:    wireValue.CompactionID,
		SourceCommandID: sourceCommandID,
		Turn:            turnValue,
		Error:           errorValue,
	}, nil
}

// DecodePrune strictly restores one compaction/prune shadow-price payload.
func DecodePrune(rawValue json.RawMessage) (Prune, error) {
	var wireValue struct {
		ShadowedRange      json.RawMessage `json:"shadowedRange"`
		ShadowedSeqs       json.RawMessage `json:"shadowedSeqs"`
		ShadowedTokenCount json.RawMessage `json:"shadowedTokenCount"`
	}
	if err := decodeStrictCompactionJSON(rawValue, &wireValue); err != nil {
		return Prune{}, fmt.Errorf("compaction: decode prune: %w", err)
	}
	shadowedRange, err := decodeRequiredRange(wireValue.ShadowedRange)
	if err != nil {
		return Prune{}, err
	}
	shadowedSeqs, err := decodeRequiredSequences(wireValue.ShadowedSeqs)
	if err != nil {
		return Prune{}, err
	}
	if len(shadowedSeqs) == 0 {
		return Prune{}, errors.New("compaction: prune shadowedSeqs must be non-empty")
	}
	if shadowedSeqs[0] != shadowedRange.Start ||
		shadowedSeqs[len(shadowedSeqs)-1] != shadowedRange.End {
		return Prune{}, errors.New(
			"compaction: prune shadowedRange must match the first and last shadowedSeqs",
		)
	}
	shadowedTokenCount, err := decodeRequiredSafeInteger(
		wireValue.ShadowedTokenCount,
		"prune shadowedTokenCount",
	)
	if err != nil {
		return Prune{}, err
	}
	return Prune{
		ShadowedRange:      shadowedRange,
		ShadowedSeqs:       shadowedSeqs,
		ShadowedTokenCount: shadowedTokenCount,
	}, nil
}

// ValidateEvent strictly checks package-owned Event payloads and ignores
// unrelated Session facts.
func ValidateEvent(entry session.Event) error {
	var err error
	switch entry.Type {
	case StartEventName:
		_, err = DecodeStart(entry.Data)
	case SummaryEventName:
		_, err = DecodeSummary(entry.Data)
	case EndEventName:
		_, err = DecodeEnd(entry.Data)
	case PruneEventName:
		_, err = DecodePrune(entry.Data)
	}
	if err != nil {
		return fmt.Errorf("compaction: invalid %s at seq %d: %w", entry.Type, entry.Seq, err)
	}
	return nil
}

func decodeStrictCompactionJSON(rawValue json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(rawValue))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("payload contains multiple JSON values")
		}
		return err
	}
	return nil
}

func decodeOptionalIdentifier(rawValue json.RawMessage, label string) (*string, error) {
	return decodeOptionalString(rawValue, label, false)
}

func decodeRequiredString(rawValue json.RawMessage, label string) (string, error) {
	if len(rawValue) == 0 {
		return "", fmt.Errorf("compaction: %s is required", label)
	}
	if bytes.Equal(bytes.TrimSpace(rawValue), []byte("null")) {
		return "", fmt.Errorf("compaction: %s cannot be null", label)
	}
	var value string
	if err := json.Unmarshal(rawValue, &value); err != nil {
		return "", fmt.Errorf("compaction: %s must be a string", label)
	}
	return value, nil
}

func decodeOptionalString(
	rawValue json.RawMessage,
	label string,
	allowEmpty bool,
) (*string, error) {
	if len(rawValue) == 0 {
		return nil, nil
	}
	if bytes.Equal(bytes.TrimSpace(rawValue), []byte("null")) {
		return nil, fmt.Errorf("compaction: %s cannot be null", label)
	}
	var value string
	if err := json.Unmarshal(rawValue, &value); err != nil {
		return nil, fmt.Errorf("compaction: %s must be a string", label)
	}
	if !allowEmpty && value == "" {
		return nil, fmt.Errorf("compaction: %s must be non-empty", label)
	}
	return &value, nil
}

func decodeRequiredNullableTurn(rawValue json.RawMessage, label string) (*int64, error) {
	if len(rawValue) == 0 {
		return nil, fmt.Errorf("compaction: %s is required", label)
	}
	if bytes.Equal(bytes.TrimSpace(rawValue), []byte("null")) {
		return nil, nil
	}
	var value int64
	if err := json.Unmarshal(rawValue, &value); err != nil || value <= 0 || value > maxSafeInteger {
		return nil, fmt.Errorf(
			"compaction: %s must be null or a positive safe integer",
			label,
		)
	}
	return &value, nil
}

func decodeRequiredContent(
	rawValue json.RawMessage,
	label string,
) ([]agentmessage.ContentBlock, error) {
	if len(rawValue) == 0 {
		return nil, fmt.Errorf("compaction: %s is required", label)
	}
	trimmed := bytes.TrimSpace(rawValue)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil, fmt.Errorf("compaction: %s must be an array", label)
	}
	blocks, err := agentmessage.DecodeContentBlocks(rawValue)
	if err != nil {
		return nil, fmt.Errorf("compaction: decode %s: %w", label, err)
	}
	return blocks, nil
}

func decodeOptionalContent(
	rawValue json.RawMessage,
	label string,
) ([]agentmessage.ContentBlock, bool, error) {
	if len(rawValue) == 0 {
		return nil, false, nil
	}
	blocks, err := decodeRequiredContent(rawValue, label)
	if err != nil {
		return nil, false, err
	}
	return blocks, true, nil
}

func decodeOptionalTrue(rawValue json.RawMessage, label string) (bool, bool, error) {
	if len(rawValue) == 0 {
		return false, false, nil
	}
	var value bool
	if err := json.Unmarshal(rawValue, &value); err != nil || !value {
		return false, false, fmt.Errorf("compaction: %s must be true when present", label)
	}
	return true, true, nil
}

func decodeRequiredRange(rawValue json.RawMessage) (SurfaceRange, error) {
	if len(rawValue) == 0 {
		return SurfaceRange{}, errors.New("compaction: shadowedRange is required")
	}
	var wireValue struct {
		Start json.RawMessage `json:"start"`
		End   json.RawMessage `json:"end"`
	}
	if err := decodeStrictCompactionJSON(rawValue, &wireValue); err != nil {
		return SurfaceRange{}, fmt.Errorf("compaction: decode shadowedRange: %w", err)
	}
	startValue, err := decodeRequiredSafeInteger(wireValue.Start, "shadowedRange start")
	if err != nil {
		return SurfaceRange{}, err
	}
	endValue, err := decodeRequiredSafeInteger(wireValue.End, "shadowedRange end")
	if err != nil {
		return SurfaceRange{}, err
	}
	return SurfaceRange{
		Start: startValue,
		End:   endValue,
	}, nil
}

func decodeRequiredSequences(rawValue json.RawMessage) ([]int64, error) {
	if len(rawValue) == 0 {
		return nil, errors.New("compaction: shadowedSeqs is required")
	}
	var sequences []int64
	if err := json.Unmarshal(rawValue, &sequences); err != nil || sequences == nil {
		return nil, errors.New("compaction: shadowedSeqs must be an array")
	}
	for _, sequence := range sequences {
		if !safeNonNegative(sequence) {
			return nil, errors.New(
				"compaction: shadowedSeqs must contain non-negative safe integers",
			)
		}
	}
	return append([]int64(nil), sequences...), nil
}

func decodeOptionalPositiveInt(rawValue json.RawMessage, label string) (*int, error) {
	if len(rawValue) == 0 {
		return nil, nil
	}
	if bytes.Equal(bytes.TrimSpace(rawValue), []byte("null")) {
		return nil, fmt.Errorf("compaction: %s cannot be null", label)
	}
	var value int64
	if err := json.Unmarshal(rawValue, &value); err != nil || value <= 0 || value > maxSafeInteger {
		return nil, fmt.Errorf("compaction: %s must be a positive safe integer", label)
	}
	converted := int(value)
	if int64(converted) != value {
		return nil, fmt.Errorf("compaction: %s exceeds this platform's int range", label)
	}
	return &converted, nil
}

func decodeOptionalUsage(rawValue json.RawMessage) (*llm.TokenUsage, error) {
	if len(rawValue) == 0 {
		return nil, nil
	}
	if bytes.Equal(bytes.TrimSpace(rawValue), []byte("null")) {
		return nil, errors.New("compaction: summary usage cannot be null")
	}
	var wireValue struct {
		InputTokens      json.RawMessage `json:"inputTokens"`
		OutputTokens     json.RawMessage `json:"outputTokens"`
		CacheReadTokens  json.RawMessage `json:"cacheReadTokens"`
		CacheWriteTokens json.RawMessage `json:"cacheWriteTokens"`
		ReasoningTokens  json.RawMessage `json:"reasoningTokens"`
	}
	if err := decodeStrictCompactionJSON(rawValue, &wireValue); err != nil {
		return nil, fmt.Errorf("compaction: decode summary usage: %w", err)
	}
	inputTokens, err := decodeRequiredSafeInteger(
		wireValue.InputTokens,
		"summary usage inputTokens",
	)
	if err != nil {
		return nil, err
	}
	outputTokens, err := decodeRequiredSafeInteger(
		wireValue.OutputTokens,
		"summary usage outputTokens",
	)
	if err != nil {
		return nil, err
	}
	cacheReadTokens, err := decodeOptionalSafeInteger(
		wireValue.CacheReadTokens,
		"summary usage cacheReadTokens",
	)
	if err != nil {
		return nil, err
	}
	cacheWriteTokens, err := decodeOptionalSafeInteger(
		wireValue.CacheWriteTokens,
		"summary usage cacheWriteTokens",
	)
	if err != nil {
		return nil, err
	}
	reasoningTokens, err := decodeOptionalSafeInteger(
		wireValue.ReasoningTokens,
		"summary usage reasoningTokens",
	)
	if err != nil {
		return nil, err
	}
	return &llm.TokenUsage{
		InputTokens:      inputTokens,
		OutputTokens:     outputTokens,
		CacheReadTokens:  cacheReadTokens,
		CacheWriteTokens: cacheWriteTokens,
		ReasoningTokens:  reasoningTokens,
	}, nil
}

func decodeRequiredSafeInteger(rawValue json.RawMessage, label string) (int64, error) {
	if len(rawValue) == 0 {
		return 0, fmt.Errorf("compaction: %s is required", label)
	}
	var value int64
	if err := json.Unmarshal(rawValue, &value); err != nil || !safeNonNegative(value) {
		return 0, fmt.Errorf(
			"compaction: %s must be a non-negative safe integer",
			label,
		)
	}
	return value, nil
}

func decodeOptionalSafeInteger(
	rawValue json.RawMessage,
	label string,
) (*int64, error) {
	if len(rawValue) == 0 {
		return nil, nil
	}
	if bytes.Equal(bytes.TrimSpace(rawValue), []byte("null")) {
		return nil, fmt.Errorf("compaction: %s cannot be null", label)
	}
	value, err := decodeRequiredSafeInteger(rawValue, label)
	if err != nil {
		return nil, err
	}
	return &value, nil
}

func safeNonNegative(value int64) bool {
	return value >= 0 && value <= maxSafeInteger
}
