package tokenmeter

import (
	"bytes"
	"encoding/json"
	"fmt"
	"unicode/utf16"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
)

const (
	charsPerToken = int64(4)
	blockOverhead = int64(4)
	roleOverhead  = int64(4)
)

func estimateContent(blocks []agentmessage.ContentBlock) (int64, error) {
	var tokens int64
	for blockIndex, contentBlock := range blocks {
		if contentBlock == nil {
			return 0, fmt.Errorf("tokenmeter: content block %d is nil", blockIndex)
		}
		var blockTokens int64
		switch typedBlock := contentBlock.(type) {
		case agentmessage.TextBlock:
			blockTokens = estimateString(typedBlock.Text) + blockOverhead
		case *agentmessage.TextBlock:
			blockTokens = estimateString(typedBlock.Text) + blockOverhead
		case agentmessage.ReasoningBlock:
			blockTokens = estimateString(typedBlock.Text) + blockOverhead
		case *agentmessage.ReasoningBlock:
			blockTokens = estimateString(typedBlock.Text) + blockOverhead
		case agentmessage.ToolCallBlock:
			blockTokens = estimateString(typedBlock.Name) +
				estimateString(typedBlock.Arguments) + blockOverhead
		case *agentmessage.ToolCallBlock:
			blockTokens = estimateString(typedBlock.Name) +
				estimateString(typedBlock.Arguments) + blockOverhead
		case agentmessage.ToolResultBlock:
			nestedTokens, err := estimateContent(typedBlock.Content)
			if err != nil {
				return 0, err
			}
			blockTokens = nestedTokens + blockOverhead
		case *agentmessage.ToolResultBlock:
			nestedTokens, err := estimateContent(typedBlock.Content)
			if err != nil {
				return 0, err
			}
			blockTokens = nestedTokens + blockOverhead
		default:
			encoded, err := encodeWithoutHTMLEscaping(contentBlock)
			if err != nil {
				return 0, fmt.Errorf(
					"tokenmeter: encode content block %d: %w",
					blockIndex,
					err,
				)
			}
			blockTokens = blockOverhead + estimateString(string(encoded))
		}
		var err error
		tokens, err = addTokens(tokens, blockTokens)
		if err != nil {
			return 0, err
		}
	}
	return tokens, nil
}

func estimateMessage(messageValue agentmessage.Message) (int64, error) {
	if messageValue == nil {
		return 0, fmt.Errorf("tokenmeter: cannot estimate a nil message")
	}
	contentTokens, err := estimateContent(messageValue.ContentValue())
	if err != nil {
		return 0, err
	}
	return addTokens(contentTokens, roleOverhead)
}

func estimateHeader(headerValue *session.EpochHeader) (int64, error) {
	if headerValue == nil {
		return 0, nil
	}
	canonical := session.CanonicalEpochHeader(*headerValue)
	systemTokens := estimateSystemTokens(&canonical)
	toolTokens, err := estimateToolsTokens(&canonical)
	if err != nil {
		return 0, err
	}
	return addTokens(systemTokens, toolTokens)
}

func estimateSystemTokens(headerValue *session.EpochHeader) int64 {
	if headerValue == nil || headerValue.System == nil {
		return 0
	}
	return estimateString(*headerValue.System) + roleOverhead
}

func estimateToolsTokens(headerValue *session.EpochHeader) (int64, error) {
	if headerValue == nil || len(headerValue.Tools) == 0 {
		return 0, nil
	}
	encoded, err := encodeWithoutHTMLEscaping(headerValue.Tools)
	if err != nil {
		return 0, fmt.Errorf("tokenmeter: encode tool schemas: %w", err)
	}
	return estimateString(string(encoded)) + blockOverhead, nil
}

func estimateString(value string) int64 {
	characterCount := int64(len(utf16.Encode([]rune(value))))
	return (characterCount + charsPerToken - 1) / charsPerToken
}

func encodeWithoutHTMLEscaping(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n")), nil
}

func addTokens(left int64, right int64) (int64, error) {
	if left < 0 || right < 0 || left > maxSafeTokenCount-right {
		return 0, fmt.Errorf("tokenmeter: token count exceeds safe integer range")
	}
	return left + right, nil
}
