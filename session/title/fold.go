package title

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

// UserMessage is one eligible human text message supplied to title providers.
type UserMessage struct {
	Seq  int64  `json:"seq"`
	Text string `json:"text"`
}

// CollectMessages returns human text-bearing user messages in log order.
func CollectMessages(events []session.Event, throughSeq *int64) ([]UserMessage, error) {
	messages := make([]UserMessage, 0)
	for _, committed := range events {
		if throughSeq != nil && committed.Seq > *throughSeq {
			break
		}
		if committed.Type != session.UserMessageEventName {
			continue
		}
		messageValue, err := llm.DecodeUserMessage(committed.Data)
		if err != nil {
			return nil, fmt.Errorf("sessiontitle: decode user/message at seq %d: %w", committed.Seq, err)
		}
		origin := messageValue.SourceValue()
		if origin == nil || origin.SourceKind() != "user" {
			continue
		}
		parts := make([]string, 0)
		for _, content := range messageValue.ContentValue() {
			textBlock, ok := content.(llm.TextBlock)
			if ok {
				parts = append(parts, textBlock.Text)
			}
		}
		text := strings.Join(parts, "\n")
		if cleanTitleText(text) == "" {
			continue
		}
		messages = append(messages, UserMessage{Seq: committed.Seq, Text: text})
	}
	return messages, nil
}

// Fold returns the latest valid session/title snapshot, or nil before one exists.
func Fold(events []session.Event) (*Snapshot, error) {
	for index := len(events) - 1; index >= 0; index-- {
		committed := events[index]
		if committed.Type != TitleEventName {
			continue
		}
		var payload EventData
		if err := json.Unmarshal(committed.Data, &payload); err != nil {
			return nil, fmt.Errorf("sessiontitle: decode session/title at seq %d: %w", committed.Seq, err)
		}
		return &Snapshot{
			EventData: EventData{
				Title: payload.Title, MessageSeqs: append([]int64{}, payload.MessageSeqs...),
				Source: payload.Source.cloneTitleSource(),
			},
			EventSeq: committed.Seq, UpdatedAt: committed.Time,
		}, nil
	}
	return nil, nil
}

func cloneSnapshot(source *Snapshot) *Snapshot {
	if source == nil {
		return nil
	}
	result := *source
	result.MessageSeqs = append([]int64{}, source.MessageSeqs...)
	result.Source = source.Source.cloneTitleSource()
	return &result
}
