package basic

import (
	"errors"
	"fmt"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/compaction"
	"github.com/gorenx/goren/llm"
)

// regionReplacement is one summarized checkpoint ready to replace its baseline.
type regionReplacement struct {
	baseline   baseline
	generated  Summary
	settings   llm.CallConfig
	checkpoint agentmessage.UserMessage
}

// messageEstimator prices a fully framed checkpoint without I/O.
type messageEstimator interface {
	EstimateMessage(agentmessage.Message) (int64, error)
}

func buildReplacement(
	compactionID compaction.ID,
	sourceCommandID *string,
	source baseline,
	generated Summary,
	settings llm.CallConfig,
	pricing messageEstimator,
) (regionReplacement, error) {
	if pricing == nil {
		return regionReplacement{}, errors.New("compaction-basic: checkpoint estimator is missing")
	}
	if err := llm.ValidateCallConfig(settings); err != nil {
		return regionReplacement{}, err
	}
	content, err := agentmessage.CloneContentBlocks(generated.Content)
	if err != nil {
		return regionReplacement{}, err
	}
	output, err := agentmessage.CloneContentBlocks(generated.Output)
	if err != nil {
		return regionReplacement{}, err
	}
	generated = Summary{
		Content: content,
		Output:  output,
		Usage:   cloneUsage(generated.Usage),
	}
	origin, err := compaction.NewCheckpointSource(
		compactionID,
		sourceCommandID,
	)
	if err != nil {
		return regionReplacement{}, err
	}
	framedSummary, err := frameSummary(generated.Content)
	if err != nil {
		return regionReplacement{}, err
	}
	checkpoint, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
		Content: framedSummary,
		Source:  origin,
	})
	if err != nil {
		return regionReplacement{}, err
	}
	checkpointTokens, err := pricing.EstimateMessage(checkpoint)
	if err != nil {
		return regionReplacement{}, err
	}
	if checkpointTokens < 0 {
		return regionReplacement{}, errors.New("compaction-basic: checkpoint estimate is negative")
	}
	if checkpointTokens >= source.tokenCount {
		return regionReplacement{}, fmt.Errorf(
			"summary is not smaller than the shadowed content (%d estimated framed tokens >= %d)",
			checkpointTokens,
			source.tokenCount,
		)
	}
	return regionReplacement{
		baseline:   source,
		generated:  generated,
		settings:   llm.CloneCallConfig(settings),
		checkpoint: checkpoint,
	}, nil
}
