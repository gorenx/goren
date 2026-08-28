package bound

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/bytedance/sonic"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
)

var interactionJSONCodec = sonic.Config{
	UseUnicodeErrors:      true,
	DisallowUnknownFields: true,
	CopyString:            true,
	ValidateString:        true,
	CaseSensitive:         true,
}.Froze()

type parentInteraction struct {
	turn        int64
	fromSeq     int64
	nextSeq     int64
	outcome     string
	content     []agentmessage.ContentBlock
	deliverable bool
}

func nextParentInteraction(
	events []session.Event,
	nextSeq int64,
) (parentInteraction, bool, error) {
	if nextSeq < 0 || nextSeq > int64(len(events)) {
		return parentInteraction{}, false, errors.New(
			"subagent: Bound cursor is outside the parent Session",
		)
	}
	current := parentInteraction{
		fromSeq: nextSeq,
	}
	var directUserMessages []agentmessage.UserMessage
	var assistantMessages []agentmessage.AssistantMessage
	for _, committed := range events[nextSeq:] {
		switch committed.Type {
		case session.TurnStartEventName:
			var started session.TurnStart
			if err := decodeInteractionJSON(committed.Data, &started); err != nil ||
				started.Turn <= 0 {
				return parentInteraction{}, false, fmt.Errorf(
					"subagent: invalid parent turn/start at seq %d",
					committed.Seq,
				)
			}
			if current.turn != 0 {
				return parentInteraction{}, false, errors.New(
					"subagent: parent Session opened a turn before closing the previous turn",
				)
			}
			current.turn = started.Turn
			directUserMessages = nil
			assistantMessages = nil
		case session.UserMessageEventName:
			if current.turn == 0 {
				continue
			}
			messageValue, err := session.DeriveEventMessage(committed)
			if err != nil {
				return parentInteraction{}, false, fmt.Errorf(
					"subagent: decode parent user/message at seq %d: %w",
					committed.Seq,
					err,
				)
			}
			typedMessage, matches := messageValue.(agentmessage.UserMessage)
			if !matches {
				continue
			}
			origin := typedMessage.SourceValue()
			if origin == nil || origin.SourceKind() != "user" {
				continue
			}
			directUserMessages = append(directUserMessages, typedMessage)
		case session.AssistantMessageEventName:
			if current.turn == 0 {
				continue
			}
			turn, messageValue, err := decodeAssistantMessage(committed)
			if err != nil {
				return parentInteraction{}, false, err
			}
			if turn != current.turn {
				return parentInteraction{}, false, fmt.Errorf(
					"subagent: parent assistant/message turn %d is inside active turn %d",
					turn,
					current.turn,
				)
			}
			if messageValue != nil {
				assistantMessages = append(assistantMessages, *messageValue)
			}
		case session.TurnEndEventName:
			var ended session.TurnEnd
			if err := decodeInteractionJSON(committed.Data, &ended); err != nil {
				return parentInteraction{}, false, fmt.Errorf(
					"subagent: invalid parent turn/end at seq %d: %w",
					committed.Seq,
					err,
				)
			}
			if current.turn == 0 {
				return parentInteraction{
					turn:        ended.Turn,
					fromSeq:     nextSeq,
					nextSeq:     committed.Seq + 1,
					outcome:     ended.Reason.TurnEndKind(),
					deliverable: false,
				}, true, nil
			}
			if ended.Turn != current.turn {
				return parentInteraction{}, false, fmt.Errorf(
					"subagent: parent turn/end %d closes active turn %d",
					ended.Turn,
					current.turn,
				)
			}
			current.nextSeq = committed.Seq + 1
			current.outcome = ended.Reason.TurnEndKind()
			if len(directUserMessages) == 0 {
				return current, true, nil
			}
			content, err := interactionContent(
				current.turn,
				current.outcome,
				directUserMessages,
				assistantMessages,
			)
			if err != nil {
				return parentInteraction{}, false, err
			}
			current.content = content
			current.deliverable = true
			return current, true, nil
		}
	}
	return parentInteraction{}, false, nil
}

func decodeAssistantMessage(
	committed session.Event,
) (int64, *agentmessage.AssistantMessage, error) {
	var wireValue struct {
		Turn    int64           `json:"turn"`
		Step    int64           `json:"step"`
		Message json.RawMessage `json:"message"`
		Usage   json.RawMessage `json:"usage,omitempty"`
	}
	if err := decodeInteractionJSON(committed.Data, &wireValue); err != nil {
		return 0, nil, fmt.Errorf(
			"subagent: decode parent assistant/message at seq %d: %w",
			committed.Seq,
			err,
		)
	}
	if wireValue.Turn <= 0 || wireValue.Step <= 0 {
		return 0, nil, fmt.Errorf(
			"subagent: parent assistant/message at seq %d has an invalid position",
			committed.Seq,
		)
	}
	messageValue, err := agentmessage.DecodeMessage(wireValue.Message)
	if err != nil {
		return 0, nil, fmt.Errorf(
			"subagent: decode parent assistant/message at seq %d: %w",
			committed.Seq,
			err,
		)
	}
	typedMessage, matches := messageValue.(agentmessage.AssistantMessage)
	if !matches {
		return 0, nil, fmt.Errorf(
			"subagent: parent assistant/message at seq %d has the wrong role",
			committed.Seq,
		)
	}
	if len(typedMessage.ContentValue()) == 0 {
		return wireValue.Turn, nil, nil
	}
	return wireValue.Turn, &typedMessage, nil
}

func interactionContent(
	turn int64,
	outcome string,
	users []agentmessage.UserMessage,
	assistants []agentmessage.AssistantMessage,
) ([]agentmessage.ContentBlock, error) {
	content := []agentmessage.ContentBlock{
		agentmessage.NewTextBlock(fmt.Sprintf(
			"Parent interaction from turn %d (outcome: %s).",
			turn,
			outcome,
		)),
	}
	for _, messageValue := range users {
		content = append(content, agentmessage.NewTextBlock("User:"))
		content = append(content, messageValue.ContentValue()...)
	}
	visibleAssistantBlocks := 0
	for _, messageValue := range assistants {
		filtered := visibleParentContent(messageValue.ContentValue())
		if len(filtered) == 0 {
			continue
		}
		content = append(content, agentmessage.NewTextBlock("Parent:"))
		content = append(content, filtered...)
		visibleAssistantBlocks += len(filtered)
	}
	if visibleAssistantBlocks == 0 {
		content = append(
			content,
			agentmessage.NewTextBlock("Parent produced no visible assistant message."),
		)
	}
	return agentmessage.CloneContentBlocks(content)
}

func visibleParentContent(
	content []agentmessage.ContentBlock,
) []agentmessage.ContentBlock {
	filtered := make([]agentmessage.ContentBlock, 0, len(content))
	for _, block := range content {
		if block == nil {
			continue
		}
		switch block.ContentType() {
		case "reasoning", "tool-call", "tool-result":
			continue
		default:
			filtered = append(filtered, block)
		}
	}
	return filtered
}

func decodeInteractionJSON(rawValue []byte, target any) error {
	return interactionJSONCodec.Unmarshal(rawValue, target)
}
