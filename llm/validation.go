package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
)

var (
	// ErrAdapterNotRegistered means no adapter constructor owns the requested wire protocol.
	ErrAdapterNotRegistered = errors.New("LLM API adapter not registered")
	// ErrAPIMismatch means an adapter was invoked with a model for another protocol.
	ErrAPIMismatch = errors.New("model API does not match adapter API")
	// ErrInvalidStream means an adapter producer returned without a terminal event.
	ErrInvalidStream = errors.New("LLM stream ended without a terminal event")
)

// ValidateModel checks model identity, routing, endpoint, and token limits.
func ValidateModel(targetModel Model) error {
	if targetModel.ID == "" {
		return errors.New("model ID is required")
	}
	if targetModel.API == "" {
		return errors.New("model API is required")
	}
	if targetModel.Provider == "" {
		return errors.New("model provider is required")
	}
	if targetModel.BaseURL == "" {
		return errors.New("model base URL is required")
	}
	baseURL, err := url.Parse(targetModel.BaseURL)
	if err != nil || baseURL.Host == "" || (baseURL.Scheme != "http" && baseURL.Scheme != "https") {
		return fmt.Errorf("invalid model base URL %q", targetModel.BaseURL)
	}
	if targetModel.ContextWindow < 0 || targetModel.MaxOutputTokens < 0 {
		return errors.New("model token limits cannot be negative")
	}
	if len(targetModel.Input) == 0 {
		return errors.New("model requires at least one input modality")
	}
	modalities := make(map[InputModality]bool, len(targetModel.Input))
	for _, modality := range targetModel.Input {
		if modality != InputText && modality != InputImage {
			return fmt.Errorf("unsupported model input modality %q", modality)
		}
		if modalities[modality] {
			return fmt.Errorf("duplicate model input modality %q", modality)
		}
		modalities[modality] = true
	}
	reasoningLevels := make(map[ReasoningLevel]bool, len(targetModel.ReasoningLevels))
	for _, level := range targetModel.ReasoningLevels {
		if !knownReasoningLevel(level) {
			return fmt.Errorf("unsupported model reasoning level %q", level)
		}
		if reasoningLevels[level] {
			return fmt.Errorf("duplicate model reasoning level %q", level)
		}
		reasoningLevels[level] = true
	}
	if !targetModel.Reasoning && (len(targetModel.ReasoningLevels) > 0 || len(targetModel.ReasoningMap) > 0 || len(targetModel.ReasoningBudget) > 0) {
		return errors.New("non-reasoning model cannot declare reasoning capabilities")
	}
	for level := range targetModel.ReasoningMap {
		if !knownReasoningLevel(level) {
			return fmt.Errorf("unsupported model reasoning mapping level %q", level)
		}
	}
	for level, budget := range targetModel.ReasoningBudget {
		if !knownReasoningLevel(level) || budget < 0 {
			return errors.New("model reasoning budgets require a level and cannot be negative")
		}
	}
	for tier, multiplier := range targetModel.ServiceTierCost {
		if tier == "" || multiplier <= 0 {
			return errors.New("model service-tier cost multipliers require a tier and must be positive")
		}
	}
	return nil
}

func knownReasoningLevel(level ReasoningLevel) bool {
	switch level {
	case ReasoningOff, ReasoningMinimal, ReasoningLow, ReasoningMedium, ReasoningHigh, ReasoningXHigh, ReasoningMax:
		return true
	default:
		return false
	}
}

// ValidateContext checks message and tool invariants before adapter execution.
func ValidateContext(input Context) error {
	_, err := validateContext(input)
	return err
}

func validateContext(input Context) (map[string]compiledToolSchema, error) {
	for index, conversationEntry := range input.Messages {
		if conversationEntry == nil {
			return nil, fmt.Errorf("message %d is nil", index)
		}
		switch value := conversationEntry.(type) {
		case UserMessage:
			if len(value.Content) == 0 {
				return nil, fmt.Errorf("user message %d has no content", index)
			}
		case AssistantMessage:
			if len(value.Content) == 0 && value.StopReason != StopReasonError && value.StopReason != StopReasonAborted {
				return nil, fmt.Errorf("assistant message %d has no content", index)
			}
		case ToolResultMessage:
			if value.ToolCallID == "" || value.ToolName == "" {
				return nil, fmt.Errorf("tool result message %d requires call ID and name", index)
			}
			if len(value.Content) == 0 {
				return nil, fmt.Errorf("tool result message %d has no content", index)
			}
		default:
			return nil, fmt.Errorf("message %d has unsupported type %T", index, conversationEntry)
		}
		if err := validateMessageContent(conversationEntry, index); err != nil {
			return nil, err
		}
	}

	toolNames := make(map[string]bool, len(input.Tools))
	compiledSchemas := make(map[string]compiledToolSchema, len(input.Tools))
	for index, toolDefinition := range input.Tools {
		if toolDefinition.Name == "" {
			return nil, fmt.Errorf("tool %d has no name", index)
		}
		if len(toolDefinition.Parameters) == 0 || !json.Valid(toolDefinition.Parameters) {
			return nil, fmt.Errorf("tool %q has invalid parameter schema", toolDefinition.Name)
		}
		if toolNames[toolDefinition.Name] {
			return nil, fmt.Errorf("tool %q is defined more than once", toolDefinition.Name)
		}
		compiledSchema, err := compileToolSchema(toolDefinition)
		if err != nil {
			return nil, err
		}
		compiledSchemas[toolDefinition.Name] = compiledToolSchema{
			parameters: string(toolDefinition.Parameters),
			validator:  compiledSchema,
		}
		toolNames[toolDefinition.Name] = true
	}
	return compiledSchemas, nil
}

func validateMessageContent(conversationEntry Message, messageIndex int) error {
	var content []any
	switch typedMessage := conversationEntry.(type) {
	case UserMessage:
		content = make([]any, len(typedMessage.Content))
		for index, contentBlock := range typedMessage.Content {
			content[index] = contentBlock
		}
	case AssistantMessage:
		if typedMessage.StopReason == StopReasonError || typedMessage.StopReason == StopReasonAborted {
			return nil
		}
		content = make([]any, len(typedMessage.Content))
		for index, contentBlock := range typedMessage.Content {
			content[index] = contentBlock
		}
	case ToolResultMessage:
		content = make([]any, len(typedMessage.Content))
		for index, contentBlock := range typedMessage.Content {
			content[index] = contentBlock
		}
	}
	for contentIndex, contentBlock := range content {
		switch typedContent := contentBlock.(type) {
		case TextContent:
		case ImageContent:
			if typedContent.MIMEType == "" || typedContent.Data == "" {
				return fmt.Errorf("message %d content %d image requires MIME type and base64 data", messageIndex, contentIndex)
			}
		case AssistantTextContent:
			switch typedContent.Phase {
			case "", AssistantTextPhaseUnspecified, AssistantTextPhaseCommentary, AssistantTextPhaseFinalAnswer:
			default:
				return fmt.Errorf("message %d content %d has unsupported assistant text phase %q", messageIndex, contentIndex, typedContent.Phase)
			}
			if err := validateReplayMetadata(typedContent.Metadata); err != nil {
				return fmt.Errorf("message %d content %d: %w", messageIndex, contentIndex, err)
			}
		case ThinkingContent:
			if err := validateReplayMetadata(typedContent.Metadata); err != nil {
				return fmt.Errorf("message %d content %d: %w", messageIndex, contentIndex, err)
			}
		case ToolCall:
			if typedContent.ID == "" || typedContent.Name == "" {
				return fmt.Errorf("message %d content %d tool call requires ID and name", messageIndex, contentIndex)
			}
			if len(typedContent.Arguments) == 0 || !json.Valid(typedContent.Arguments) {
				return fmt.Errorf("message %d content %d tool call has invalid arguments", messageIndex, contentIndex)
			}
			if err := validateReplayMetadata(typedContent.Metadata); err != nil {
				return fmt.Errorf("message %d content %d: %w", messageIndex, contentIndex, err)
			}
		default:
			return fmt.Errorf("message %d content %d has unsupported type %T", messageIndex, contentIndex, contentBlock)
		}
	}
	return nil
}

// ValidateToolSelection checks selection semantics against this invocation's tools.
func ValidateToolSelection(toolDefinitions []Tool, invocationOptions StreamOptions) error {
	toolSelection := invocationOptions.ToolChoice
	if toolSelection == nil || toolSelection.Mode == ToolChoiceNone || toolSelection.Mode == ToolChoiceAuto {
		return nil
	}
	if len(toolDefinitions) == 0 {
		return errors.New("tool choice requires at least one tool")
	}
	if toolSelection.Mode != ToolChoiceFunction {
		return nil
	}
	for _, toolDefinition := range toolDefinitions {
		if toolDefinition.Name == toolSelection.Name {
			return nil
		}
	}
	return fmt.Errorf("tool choice references undefined function %q", toolSelection.Name)
}

func validateReplayMetadata(metadata *ReplayMetadata) error {
	if metadata == nil {
		return nil
	}
	if metadata.API == "" || metadata.Provider == "" || metadata.Model == "" {
		return errors.New("replay metadata requires API, provider, and model")
	}
	if len(metadata.Data) == 0 || !json.Valid(metadata.Data) {
		return errors.New("replay metadata data must be valid JSON")
	}
	return nil
}

// ValidateOptions checks invocation controls that are shared by adapters.
func ValidateOptions(invocationOptions StreamOptions) error {
	if invocationOptions.Temperature != nil && (*invocationOptions.Temperature < 0 || *invocationOptions.Temperature > 2) {
		return errors.New("temperature must be between 0 and 2")
	}
	if invocationOptions.MaxOutputTokens < 0 {
		return errors.New("max output tokens cannot be negative")
	}
	switch invocationOptions.Reasoning {
	case "", ReasoningOff, ReasoningMinimal, ReasoningLow, ReasoningMedium, ReasoningHigh, ReasoningXHigh, ReasoningMax:
	default:
		return fmt.Errorf("unsupported reasoning level %q", invocationOptions.Reasoning)
	}
	switch invocationOptions.ReasoningSummary {
	case "", ReasoningSummaryAuto, ReasoningSummaryConcise, ReasoningSummaryDetailed:
	default:
		return fmt.Errorf("unsupported reasoning summary %q", invocationOptions.ReasoningSummary)
	}
	if invocationOptions.ThinkingBudget < 0 {
		return errors.New("thinking budget cannot be negative")
	}
	if invocationOptions.Timeout < 0 || invocationOptions.MaxRetryDelay < 0 {
		return errors.New("timeout and max retry delay cannot be negative")
	}
	if invocationOptions.MaxRetries != nil && *invocationOptions.MaxRetries < 0 {
		return errors.New("max retries cannot be negative")
	}
	if len(invocationOptions.RequestID) > 512 {
		return errors.New("request ID cannot exceed 512 ASCII bytes")
	}
	for index := 0; index < len(invocationOptions.RequestID); index++ {
		if invocationOptions.RequestID[index] < 0x20 || invocationOptions.RequestID[index] > 0x7e {
			return errors.New("request ID must contain only printable ASCII characters")
		}
	}
	if invocationOptions.CacheRetention != "" &&
		invocationOptions.CacheRetention != CacheRetentionMemory &&
		invocationOptions.CacheRetention != CacheRetention24Hours {
		return fmt.Errorf("unsupported cache retention %q", invocationOptions.CacheRetention)
	}
	if toolSelection := invocationOptions.ToolChoice; toolSelection != nil {
		switch toolSelection.Mode {
		case ToolChoiceAuto, ToolChoiceNone, ToolChoiceRequired:
			if toolSelection.Name != "" {
				return errors.New("tool choice name is only valid for function mode")
			}
		case ToolChoiceFunction:
			if toolSelection.Name == "" {
				return errors.New("function tool choice requires a name")
			}
		default:
			return fmt.Errorf("unsupported tool choice %q", toolSelection.Mode)
		}
	}
	if responseSchema := invocationOptions.ResponseFormat; responseSchema != nil {
		if responseSchema.Name == "" {
			return errors.New("response format name is required")
		}
		if len(responseSchema.Schema) == 0 || !json.Valid(responseSchema.Schema) {
			return errors.New("response format schema is invalid")
		}
	}
	return nil
}

// ValidateModelOptions checks invocation controls against one model's limits
// and declared capabilities.
func ValidateModelOptions(targetModel Model, invocationOptions StreamOptions) error {
	if targetModel.MaxOutputTokens > 0 && invocationOptions.MaxOutputTokens > targetModel.MaxOutputTokens {
		return fmt.Errorf("max output tokens %d exceed model limit %d", invocationOptions.MaxOutputTokens, targetModel.MaxOutputTokens)
	}
	if invocationOptions.ReasoningSummary != "" && (invocationOptions.Reasoning == "" || invocationOptions.Reasoning == ReasoningOff) {
		return errors.New("reasoning summary requires enabled reasoning")
	}
	if invocationOptions.ThinkingBudget > 0 && !targetModel.Reasoning {
		return errors.New("thinking budget requires a reasoning model")
	}
	_, _, _, err := ResolveReasoning(targetModel, invocationOptions.Reasoning)
	return err
}

// ValidateAssistantToolCalls validates every complete ToolCall in a message.
func ValidateAssistantToolCalls(toolDefinitions []Tool, assistantReply AssistantMessage) error {
	for _, contentBlock := range assistantReply.Content {
		requestedCall, ok := contentBlock.(ToolCall)
		if !ok {
			continue
		}
		if err := ValidateToolCall(toolDefinitions, requestedCall); err != nil {
			return err
		}
	}
	return nil
}
