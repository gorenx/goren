package basic

import (
	"context"
	"fmt"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/compaction"
)

// regionReplacement is one summarized checkpoint ready to replace its baseline.
type regionReplacement struct {
	baseline   baseline
	generated  summaryResult
	checkpoint agentmessage.UserMessage
}

func (owner *regionCompactor) buildReplacement(
	requestContext context.Context,
	ownerContext compaction.AgentContext,
	compactionID compaction.ID,
	sourceCommandID *string,
	source baseline,
) (regionReplacement, error) {
	generated, err := owner.summarizer.summarize(
		requestContext,
		source.input,
		ownerContext,
	)
	if err != nil {
		return regionReplacement{}, err
	}
	origin, err := compaction.NewCheckpointSource(
		compactionID,
		sourceCommandID,
	)
	if err != nil {
		return regionReplacement{}, err
	}
	framedSummary, err := frameSummary(generated.summary)
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
	checkpointTokens, err := owner.meter.EstimateMessage(checkpoint)
	if err != nil {
		return regionReplacement{}, err
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
		checkpoint: checkpoint,
	}, nil
}
