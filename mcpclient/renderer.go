package mcpclient

import (
	"encoding/json"
	"strings"

	"github.com/gorenx/goren/agentmessage"
)

// renderer projects readable text while preserving unknown MCP blocks as JSON.
type renderer struct{}

func (*renderer) Render(_ json.RawMessage, value json.RawMessage) ([]agentmessage.ContentBlock, error) {
	var result struct {
		Content           []json.RawMessage `json:"content"`
		StructuredContent json.RawMessage   `json:"structuredContent"`
	}
	if err := json.Unmarshal(value, &result); err != nil {
		return nil, err
	}
	parts := make([]string, 0, len(result.Content)+1)
	for _, rawBlock := range result.Content {
		var block struct {
			Type string  `json:"type"`
			Text *string `json:"text"`
		}
		if json.Unmarshal(rawBlock, &block) == nil &&
			block.Type == "text" &&
			block.Text != nil {
			parts = append(parts, *block.Text)
		} else {
			parts = append(parts, string(rawBlock))
		}
	}
	if len(parts) == 0 && len(result.StructuredContent) != 0 {
		parts = append(parts, string(result.StructuredContent))
	}
	return []agentmessage.ContentBlock{agentmessage.NewTextBlock(strings.Join(parts, "\n"))}, nil
}
