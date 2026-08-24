package tokenmeter

import (
	"encoding/json"
	"errors"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	sessionprojection "github.com/gorenx/goren/session/projection"
)

// TokenUsageProjectionKey is the canonical Session projection key.
const TokenUsageProjectionKey = "tokenUsage"

type usageSample struct {
	Turn    int64                `json:"turn"`
	Step    int64                `json:"step"`
	Buckets TokenUsageProjection `json:"buckets"`
}

type tokenUsageState struct {
	Totals TokenUsageProjection `json:"totals"`
	Last   *usageSample         `json:"last"`
}

type tokenUsageUnit struct{}

func (tokenUsageUnit) Key() string { return TokenUsageProjectionKey }

func (tokenUsageUnit) StateVersion() int64 { return 1 }

func (tokenUsageUnit) InitialState() (json.RawMessage, error) {
	return encodeProjectionState(tokenUsageState{})
}

func (tokenUsageUnit) ApplyState(
	rawState json.RawMessage,
	entry session.Event,
) (sessionprojection.Transition, error) {
	var current tokenUsageState
	if err := decodeProjectionState(rawState, &current); err != nil {
		return sessionprojection.Transition{}, err
	}
	turn, step, usageValue, found, err := usageSampleFromEvent(entry)
	if err != nil {
		return sessionprojection.Transition{}, err
	}
	if !found {
		return unchangedProjection(rawState), nil
	}
	buckets, err := usageBuckets(usageValue)
	if err != nil {
		return sessionprojection.Transition{}, err
	}
	if current.Last != nil && current.Last.Turn == turn &&
		current.Last.Step == step && current.Last.Buckets == buckets {
		return unchangedProjection(rawState), nil
	}
	var previous *TokenUsageProjection
	if current.Last != nil && current.Last.Turn == turn && current.Last.Step == step {
		previousValue := current.Last.Buckets
		previous = &previousValue
	}
	nextTotals, err := replaceUsageBuckets(current.Totals, previous, buckets)
	if err != nil {
		return sessionprojection.Transition{}, err
	}
	next := tokenUsageState{
		Totals: nextTotals,
		Last: &usageSample{
			Turn:    turn,
			Step:    step,
			Buckets: buckets,
		},
	}
	return changedProjection(next)
}

func (tokenUsageUnit) ViewState(rawState json.RawMessage) (json.RawMessage, error) {
	var current tokenUsageState
	if err := decodeProjectionState(rawState, &current); err != nil {
		return nil, err
	}
	return encodeProjectionState(current.Totals)
}

func usageSampleFromEvent(
	entry session.Event,
) (int64, int64, llm.TokenUsage, bool, error) {
	switch entry.Type {
	case session.AssistantChunkEventName:
		facts, err := decodeAssistantChunkFacts(entry)
		if err != nil {
			return 0, 0, llm.TokenUsage{}, false, err
		}
		var usageValue llm.TokenUsage
		switch typedChunk := facts.chunk.(type) {
		case llm.UsageChunk:
			usageValue = typedChunk.Usage
		case *llm.UsageChunk:
			usageValue = typedChunk.Usage
		default:
			return 0, 0, llm.TokenUsage{}, false, nil
		}
		if err := validateUsage(usageValue); err != nil {
			return 0, 0, llm.TokenUsage{}, false, err
		}
		return facts.turn, facts.step, usageValue, true, nil
	case session.AssistantMessageEventName:
		facts, err := decodeAssistantMessageFacts(entry)
		if err != nil {
			return 0, 0, llm.TokenUsage{}, false, err
		}
		if facts.usage == nil {
			return 0, 0, llm.TokenUsage{}, false, nil
		}
		return facts.turn, facts.step, *facts.usage, true, nil
	default:
		return 0, 0, llm.TokenUsage{}, false, nil
	}
}

func usageBuckets(usageValue llm.TokenUsage) (TokenUsageProjection, error) {
	if err := validateUsage(usageValue); err != nil {
		return TokenUsageProjection{}, err
	}
	return TokenUsageProjection{
		UncachedInputTokens: usageValue.InputTokens,
		OutputTokens:        usageValue.OutputTokens,
		CacheReadTokens:     optionalTokenCount(usageValue.CacheReadTokens),
		CacheWriteTokens:    optionalTokenCount(usageValue.CacheWriteTokens),
	}, nil
}

func replaceUsageBuckets(
	totals TokenUsageProjection,
	previous *TokenUsageProjection,
	next TokenUsageProjection,
) (TokenUsageProjection, error) {
	uncachedInput, err := replaceBucket(
		totals.UncachedInputTokens,
		previousBucket(previous, func(value TokenUsageProjection) int64 {
			return value.UncachedInputTokens
		}),
		next.UncachedInputTokens,
	)
	if err != nil {
		return TokenUsageProjection{}, err
	}
	output, err := replaceBucket(
		totals.OutputTokens,
		previousBucket(previous, func(value TokenUsageProjection) int64 {
			return value.OutputTokens
		}),
		next.OutputTokens,
	)
	if err != nil {
		return TokenUsageProjection{}, err
	}
	cacheRead, err := replaceBucket(
		totals.CacheReadTokens,
		previousBucket(previous, func(value TokenUsageProjection) int64 {
			return value.CacheReadTokens
		}),
		next.CacheReadTokens,
	)
	if err != nil {
		return TokenUsageProjection{}, err
	}
	cacheWrite, err := replaceBucket(
		totals.CacheWriteTokens,
		previousBucket(previous, func(value TokenUsageProjection) int64 {
			return value.CacheWriteTokens
		}),
		next.CacheWriteTokens,
	)
	if err != nil {
		return TokenUsageProjection{}, err
	}
	return TokenUsageProjection{
		UncachedInputTokens: uncachedInput,
		OutputTokens:        output,
		CacheReadTokens:     cacheRead,
		CacheWriteTokens:    cacheWrite,
	}, nil
}

func previousBucket(
	previous *TokenUsageProjection,
	selectValue func(TokenUsageProjection) int64,
) int64 {
	if previous == nil {
		return 0
	}
	return selectValue(*previous)
}

func replaceBucket(total int64, previous int64, next int64) (int64, error) {
	if total < previous {
		return 0, errors.New("tokenmeter: usage projection total is smaller than prior sample")
	}
	return addTokens(total-previous, next)
}

func optionalTokenCount(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

var _ sessionprojection.Unit = tokenUsageUnit{}
