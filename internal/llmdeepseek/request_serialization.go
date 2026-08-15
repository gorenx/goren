package llmdeepseek

import (
	"encoding/json"
	"fmt"

	"github.com/gorenx/goren/llm"
)

type resolvedThinking struct {
	thinking *ThinkingMode
	effort   *ReasoningEffort
}

// SerializeRequest builds the complete direct-provider request. Optional
// sampling fields retain omission semantics from GenerateOptions.
func SerializeRequest(requestOptions llm.GenerateOptions, defaults RequestDefaults) (wireRequest, error) {
	wireMessages, err := SerializeMessages(requestOptions.Messages)
	if err != nil {
		return wireRequest{}, err
	}
	if requestOptions.System != nil {
		wireMessages = append([]wireMessage{
			wireSystemMessage{Role: "system", Content: *requestOptions.System},
		}, wireMessages...)
	}
	thinking, err := resolveThinking(requestOptions, defaults)
	if err != nil {
		return wireRequest{}, err
	}
	wireTools := make([]wireTool, 0, len(requestOptions.Tools))
	for toolIndex, schema := range requestOptions.Tools {
		if len(schema.Parameters) == 0 || !json.Valid(schema.Parameters) {
			return wireRequest{}, fmt.Errorf("llm-deepseek: tool %d parameters must be valid JSON", toolIndex)
		}
		wireTools = append(wireTools, wireTool{
			Type: "function",
			Function: wireToolDefinition{
				Name: schema.Name, Description: schema.Description,
				Parameters: append(json.RawMessage(nil), schema.Parameters...),
			},
		})
	}
	request := wireRequest{
		Model: requestOptions.Model, Messages: wireMessages, Stream: true,
		StreamOptions: wireStreamOptions{IncludeUsage: true},
		Temperature:   requestOptions.Temperature, MaxTokens: requestOptions.MaxTokens,
	}
	if thinking.thinking != nil {
		request.Thinking = &wireThinking{Type: *thinking.thinking}
	}
	request.ReasoningEffort = thinking.effort
	if len(wireTools) > 0 {
		request.Tools = wireTools
	}
	if requestOptions.Stop != nil {
		stopSequences := append([]string{}, requestOptions.Stop...)
		request.Stop = &stopSequences
	}
	return request, nil
}

func resolveThinking(requestOptions llm.GenerateOptions, defaults RequestDefaults) (resolvedThinking, error) {
	if requestOptions.Purpose == llm.PurposeSessionTitle {
		return resolvedThinking{thinking: thinkingPointer(ThinkingDisabled)}, nil
	}
	effort := defaults.ReasoningEffort
	if requestOptions.ReasoningEffort != "" {
		candidate := ReasoningEffort(requestOptions.ReasoningEffort)
		switch candidate {
		case ReasoningOff, ReasoningHigh, ReasoningMax:
			effort = &candidate
		default:
			return resolvedThinking{}, llm.MustLlmError(
				fmt.Sprintf("DeepSeek does not support reasoning effort %q", requestOptions.ReasoningEffort),
				"UNSUPPORTED_REASONING_EFFORT",
			)
		}
	}
	if defaults.Thinking != nil && *defaults.Thinking == ThinkingDisabled && effort != nil && *effort != ReasoningOff {
		return resolvedThinking{}, llm.MustLlmError(
			fmt.Sprintf("DeepSeek deployment does not support reasoning effort %q", *effort),
			"UNSUPPORTED_REASONING_EFFORT",
		)
	}
	if effort != nil {
		switch *effort {
		case ReasoningOff:
			return resolvedThinking{thinking: thinkingPointer(ThinkingDisabled)}, nil
		case ReasoningHigh, ReasoningMax:
			wireEffort := *effort
			return resolvedThinking{thinking: thinkingPointer(ThinkingEnabled), effort: &wireEffort}, nil
		}
	}
	return resolvedThinking{thinking: cloneThinking(defaults.Thinking)}, nil
}

func thinkingPointer(value ThinkingMode) *ThinkingMode { return &value }
