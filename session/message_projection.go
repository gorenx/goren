package session

import (
	"encoding/json"
	"errors"

	"github.com/gorenx/goren/agentmessage"
)

func decodeDerivedMessage(entry Event) (agentmessage.Message, error) {
	switch entry.Type {
	case UserMessageEventName:
		return agentmessage.DecodeUserMessage(entry.Data)
	case AssistantMessageEventName:
		var wireValue struct {
			Turn    int64           `json:"turn"`
			Step    int64           `json:"step"`
			Message json.RawMessage `json:"message"`
			Usage   json.RawMessage `json:"usage,omitempty"`
		}
		if err := decodeSessionPayload(entry.Data, &wireValue); err != nil {
			return nil, err
		}
		messageValue, err := agentmessage.DecodeMessage(wireValue.Message)
		if err != nil {
			return nil, err
		}
		typedMessage, ok := messageValue.(agentmessage.AssistantMessage)
		if !ok {
			return nil, errors.New("session: assistant/message contains a non-assistant message")
		}
		if len(typedMessage.ContentValue()) == 0 {
			return nil, nil
		}
		return typedMessage, nil
	case ToolResultEventName:
		var wireValue struct {
			Message json.RawMessage `json:"message"`
		}
		// Tool result data is merge-extensible. Projection needs only the owned
		// message field and must not discard or reject provider/plugin metadata.
		if err := json.Unmarshal(entry.Data, &wireValue); err != nil {
			return nil, err
		}
		messageValue, err := agentmessage.DecodeMessage(wireValue.Message)
		if err != nil {
			return nil, err
		}
		typedMessage, ok := messageValue.(agentmessage.ToolResultMessage)
		if !ok {
			return nil, errors.New("session: tool/result contains a non-tool-result message")
		}
		return typedMessage, nil
	default:
		return nil, nil
	}
}

// DeriveEventMessage projects one detached Session event through the same
// owner-defined mapping used by Surface reconstruction. Non-message events and
// empty assistant anchors return nil without an error.
func DeriveEventMessage(entry Event) (agentmessage.Message, error) {
	return decodeDerivedMessage(cloneEvent(entry))
}
