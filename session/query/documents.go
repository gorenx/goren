package query

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
)

// BuildDocuments projects one already validated Session log into semantic
// documents. Structural events and raw reasoning/chunks are intentionally not
// searchable.
func BuildDocuments(identifier session.SessionID, entries []session.Event) ([]Document, error) {
	current := currentSurface(entries)
	result := make([]Document, 0, len(entries))
	for _, entry := range entries {
		textValue, err := extractEventText(entry)
		if err != nil {
			return nil, fmt.Errorf("session query: extract %s at seq %d: %w", entry.Type, entry.Seq, err)
		}
		if textValue == "" {
			continue
		}
		placement := SurfaceLogOnly
		if entry.SurfaceOp != nil {
			if _, retained := current[entry.Seq]; retained {
				placement = SurfaceCurrent
			} else {
				placement = SurfaceShadowed
			}
		}
		result = append(result, Document{
			SessionID: identifier, Seq: entry.Seq, Type: entry.Type,
			Time: entry.Time, Surface: placement, Text: textValue,
		})
	}
	return result, nil
}

func currentSurface(entries []session.Event) map[int64]struct{} {
	nodes := make([]int64, 0)
	for _, entry := range entries {
		if entry.SurfaceOp == nil {
			continue
		}
		switch entry.SurfaceOp.Kind {
		case session.SurfaceOperationAppend:
			nodes = append(nodes, entry.Seq)
		case session.SurfaceOperationReplace:
			start := sequencePosition(nodes, entry.SurfaceOp.Start)
			end := sequencePosition(nodes, entry.SurfaceOp.End)
			if start >= 0 && end >= start {
				replaced := make([]int64, 0, len(nodes)-(end-start)+1)
				replaced = append(replaced, nodes[:start]...)
				replaced = append(replaced, entry.Seq)
				replaced = append(replaced, nodes[end+1:]...)
				nodes = replaced
			}
		}
	}
	result := make(map[int64]struct{}, len(nodes))
	for _, sequence := range nodes {
		result[sequence] = struct{}{}
	}
	return result
}

func sequencePosition(nodes []int64, target int64) int {
	for position, sequence := range nodes {
		if sequence == target {
			return position
		}
	}
	return -1
}

func extractEventText(entry session.Event) (string, error) {
	switch entry.Type {
	case session.UserMessageEventName:
		messageValue, err := agentmessage.DecodeUserMessage(entry.Data)
		if err != nil {
			return "", err
		}
		return contentText(messageValue.ContentValue()), nil
	case session.AssistantMessageEventName:
		var payload struct {
			Message json.RawMessage `json:"message"`
		}
		if err := json.Unmarshal(entry.Data, &payload); err != nil {
			return "", err
		}
		messageValue, err := agentmessage.DecodeMessage(payload.Message)
		if err != nil {
			return "", err
		}
		return contentText(messageValue.ContentValue()), nil
	case session.ToolCallEventName:
		var payload session.ToolCall
		if err := json.Unmarshal(entry.Data, &payload); err != nil {
			return "", err
		}
		return joinText(payload.Name, payload.Arguments), nil
	case session.ToolResultEventName:
		var payload struct {
			Message json.RawMessage        `json:"message"`
			Error   *session.ToolErrorInfo `json:"error,omitempty"`
		}
		if err := json.Unmarshal(entry.Data, &payload); err != nil {
			return "", err
		}
		messageValue, err := agentmessage.DecodeMessage(payload.Message)
		if err != nil {
			return "", err
		}
		parts := []string{contentText(messageValue.ContentValue())}
		if payload.Error != nil {
			parts = append(parts, payload.Error.Name, payload.Error.Code)
		}
		return joinText(parts...), nil
	case session.TurnEndEventName:
		var payload struct {
			Reason json.RawMessage `json:"reason"`
		}
		if err := json.Unmarshal(entry.Data, &payload); err != nil {
			return "", err
		}
		var reason struct {
			Kind  string `json:"kind"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error,omitempty"`
		}
		if err := json.Unmarshal(payload.Reason, &reason); err != nil {
			return "", err
		}
		switch reason.Kind {
		case "error":
			if reason.Error == nil {
				return "", errors.New("error turn end is missing error metadata")
			}
			return joinText("error", reason.Error.Message), nil
		case "aborted", "max-tokens", "interrupted":
			return reason.Kind, nil
		default:
			return "", nil
		}
	case "todo/write":
		var payload struct {
			Todos []struct {
				Status  string `json:"status"`
				Content string `json:"content"`
			} `json:"todos"`
		}
		if err := json.Unmarshal(entry.Data, &payload); err != nil {
			return "", err
		}
		parts := make([]string, 0, len(payload.Todos)*2)
		for _, item := range payload.Todos {
			parts = append(parts, item.Status, item.Content)
		}
		return joinText(parts...), nil
	default:
		return "", nil
	}
}

func contentText(content []agentmessage.ContentBlock) string {
	parts := make([]string, 0, len(content))
	for _, block := range content {
		switch typed := block.(type) {
		case agentmessage.PlainTextContent:
			if textValue, available := typed.PlainText(); available {
				parts = append(parts, textValue)
			}
		case agentmessage.ToolCallBlock:
			parts = append(parts, typed.Name, typed.Arguments)
		case agentmessage.ToolResultBlock:
			parts = append(parts, contentText(typed.Content))
		}
	}
	return joinText(parts...)
}

func joinText(parts ...string) string {
	owned := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			owned = append(owned, trimmed)
		}
	}
	return strings.Join(owned, "\n")
}
