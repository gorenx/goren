package turnrelay

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/bytedance/sonic"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	boundcontract "github.com/gorenx/goren/subagent/bound"
)

const turnSourceKind = "session-turn"

type interaction struct {
	turn        int64
	fromSeq     int64
	nextSeq     int64
	outcome     string
	content     []agentmessage.ContentBlock
	deliverable bool
}

// Turn identifies the committed Session turn represented by one Bound Input.
type Turn struct {
	Kind       string            `json:"kind"`
	SessionID  session.SessionID `json:"sessionId"`
	Turn       int64             `json:"turn"`
	FromSeq    int64             `json:"fromSeq"`
	ThroughSeq int64             `json:"throughSeq"`
	Outcome    string            `json:"outcome"`
}

func (Turn) SourceKind() string {
	return turnSourceKind
}

func (source Turn) CloneSource() (agentmessage.MessageSource, error) {
	if source.SessionID == "" || source.Turn <= 0 || source.FromSeq < 0 ||
		source.ThroughSeq < source.FromSeq || !validOutcome(source.Outcome) {
		return nil, errors.New(
			"subagent/bound/turnrelay: invalid Session Turn",
		)
	}
	source.Kind = turnSourceKind
	return source, nil
}

func (current interaction) input(
	parentID session.SessionID,
) (boundcontract.Input, error) {
	origin, err := (Turn{
		SessionID:  parentID,
		Turn:       current.turn,
		FromSeq:    current.fromSeq,
		ThroughSeq: current.nextSeq - 1,
		Outcome:    current.outcome,
	}).CloneSource()
	if err != nil {
		return boundcontract.Input{}, err
	}
	return boundcontract.Input{
		ID: boundcontract.InputID(fmt.Sprintf(
			"session:%s:seq:%d-%d",
			parentID,
			current.fromSeq,
			current.nextSeq-1,
		)),
		Content: current.content,
		Source:  origin,
	}, nil
}

func nextInteraction(
	events []session.Event,
	nextSeq int64,
) (interaction, bool, error) {
	if nextSeq < 0 || nextSeq > int64(len(events)) {
		return interaction{}, false, errors.New(
			"subagent/bound/turnrelay: cursor is outside the Session",
		)
	}
	current := interaction{
		fromSeq: nextSeq,
	}
	var directUserMessages []agentmessage.UserMessage
	var assistantMessages []agentmessage.AssistantMessage
	for _, committed := range events[nextSeq:] {
		switch committed.Type {
		case session.TurnStartEventName:
			var started session.TurnStart
			if err := turnCodec.Unmarshal(committed.Data, &started); err != nil ||
				started.Turn <= 0 {
				return interaction{}, false, fmt.Errorf(
					"subagent/bound/turnrelay: invalid turn/start at seq %d",
					committed.Seq,
				)
			}
			if current.turn != 0 {
				return interaction{}, false, errors.New(
					"subagent/bound/turnrelay: Session opened a turn before closing the previous turn",
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
				return interaction{}, false, fmt.Errorf(
					"subagent/bound/turnrelay: decode user/message at seq %d: %w",
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
			turnNumber, messageValue, err := decodeAssistantMessage(committed)
			if err != nil {
				return interaction{}, false, err
			}
			if turnNumber != current.turn {
				return interaction{}, false, fmt.Errorf(
					"subagent/bound/turnrelay: assistant/message turn %d is inside active turn %d",
					turnNumber,
					current.turn,
				)
			}
			if messageValue != nil {
				assistantMessages = append(assistantMessages, *messageValue)
			}
		case session.TurnEndEventName:
			var ended session.TurnEnd
			if err := turnCodec.Unmarshal(committed.Data, &ended); err != nil {
				return interaction{}, false, fmt.Errorf(
					"subagent/bound/turnrelay: invalid turn/end at seq %d: %w",
					committed.Seq,
					err,
				)
			}
			if current.turn == 0 {
				return interaction{
					turn:        ended.Turn,
					fromSeq:     nextSeq,
					nextSeq:     committed.Seq + 1,
					outcome:     ended.Reason.TurnEndKind(),
					deliverable: false,
				}, true, nil
			}
			if ended.Turn != current.turn {
				return interaction{}, false, fmt.Errorf(
					"subagent/bound/turnrelay: turn/end %d closes active turn %d",
					ended.Turn,
					current.turn,
				)
			}
			current.nextSeq = committed.Seq + 1
			current.outcome = ended.Reason.TurnEndKind()
			if len(directUserMessages) == 0 {
				return current, true, nil
			}
			content := interactionContent(
				current.turn,
				current.outcome,
				directUserMessages,
				assistantMessages,
			)
			current.content = content
			current.deliverable = true
			return current, true, nil
		}
	}
	return interaction{}, false, nil
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
	if err := turnCodec.Unmarshal(committed.Data, &wireValue); err != nil {
		return 0, nil, fmt.Errorf(
			"subagent/bound/turnrelay: decode assistant/message at seq %d: %w",
			committed.Seq,
			err,
		)
	}
	if wireValue.Turn <= 0 || wireValue.Step <= 0 {
		return 0, nil, fmt.Errorf(
			"subagent/bound/turnrelay: assistant/message at seq %d has an invalid position",
			committed.Seq,
		)
	}
	messageValue, err := agentmessage.DecodeMessage(wireValue.Message)
	if err != nil {
		return 0, nil, fmt.Errorf(
			"subagent/bound/turnrelay: decode assistant/message at seq %d: %w",
			committed.Seq,
			err,
		)
	}
	typedMessage, matches := messageValue.(agentmessage.AssistantMessage)
	if !matches {
		return 0, nil, fmt.Errorf(
			"subagent/bound/turnrelay: assistant/message at seq %d has the wrong role",
			committed.Seq,
		)
	}
	if len(typedMessage.ContentValue()) == 0 {
		return wireValue.Turn, nil, nil
	}
	return wireValue.Turn, &typedMessage, nil
}

func interactionContent(
	turnNumber int64,
	outcome string,
	users []agentmessage.UserMessage,
	assistants []agentmessage.AssistantMessage,
) []agentmessage.ContentBlock {
	content := []agentmessage.ContentBlock{
		agentmessage.NewTextBlock(fmt.Sprintf(
			"Parent interaction from turn %d (outcome: %s).",
			turnNumber,
			outcome,
		)),
	}
	for _, messageValue := range users {
		content = append(content, agentmessage.NewTextBlock("User:"))
		content = append(content, messageValue.ContentValue()...)
	}
	visibleAssistantBlocks := 0
	for _, messageValue := range assistants {
		filtered := visibleContent(messageValue.ContentValue())
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
			agentmessage.NewTextBlock("Parent produced no visible reply."),
		)
	}
	return content
}

func visibleContent(
	content []agentmessage.ContentBlock,
) []agentmessage.ContentBlock {
	filtered := make([]agentmessage.ContentBlock, 0, len(content))
	for _, block := range content {
		switch block.(type) {
		case agentmessage.ReasoningBlock,
			agentmessage.ToolCallBlock,
			agentmessage.ToolResultBlock:
			continue
		default:
			filtered = append(filtered, block)
		}
	}
	return filtered
}

func validOutcome(outcome string) bool {
	switch outcome {
	case "completed", "blocked", "max-tokens", "interrupted", "aborted", "error":
		return true
	default:
		return false
	}
}

var turnCodec = sonic.Config{
	UseUnicodeErrors:      true,
	DisallowUnknownFields: true,
	CopyString:            true,
	ValidateString:        true,
	CaseSensitive:         true,
}.Froze()

var _ agentmessage.MessageSource = Turn{}
