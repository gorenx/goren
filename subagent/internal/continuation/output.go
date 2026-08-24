package continuation

import (
	"errors"
	"fmt"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent/internal/assistantoutput"
)

func lastAssistant(
	conversation session.Context,
	boundary int64,
) ([]llm.ContentBlock, error) {
	if conversation == nil {
		return nil, errors.New("subagent: child Session is nil")
	}
	events := conversation.Events()
	if boundary < 0 || boundary > conversation.Seq() {
		return nil, fmt.Errorf(
			"subagent: invalid Activation boundary %d",
			boundary,
		)
	}
	suffix := make([]session.Event, 0, len(events))
	for _, committed := range events {
		if committed.Seq >= boundary {
			suffix = append(suffix, committed)
		}
	}
	return assistantoutput.Select(suffix)
}
