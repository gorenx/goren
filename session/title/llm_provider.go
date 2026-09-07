package title

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

const (
	FirstPromptLLMProviderID ProviderID = "session-title-first-prompt-llm"
	AllPromptsLLMProviderID  ProviderID = "session-title-all-prompts-llm"
	TitleLLMRequestEventName            = "session/title-llm-request"
	TitleLLMTimeoutCode                 = "SESSION_TITLE_TIMEOUT"
)

// LLMStreamer is the title capability's narrow outbound dependency on the
// provider-neutral LLM runtime.
type LLMStreamer interface {
	Stream(context.Context, llm.GenerateOptions) (llm.ChunkStream, error)
}

// LLMConfig is the complete policy for one model-backed title Provider.
type LLMConfig struct {
	AutomaticMode       AutomaticMode `json:"automaticMode"`
	TargetWords         int           `json:"targetWords"`
	TargetCJKCharacters int           `json:"targetCjkCharacters"`
	MaxInputBytes       int           `json:"maxInputBytes"`
	MaxOutputTokens     int           `json:"maxOutputTokens"`
	TimeoutMS           int64         `json:"timeoutMs"`
	Provider            string        `json:"provider,omitempty"`
	Model               string        `json:"model,omitempty"`
}

// Validate checks the complete auxiliary-call policy without supplying
// library defaults.
func (settings LLMConfig) Validate() (LLMConfig, error) {
	if settings.AutomaticMode != AutomaticFirstPrompt && settings.AutomaticMode != AutomaticAllPrompts {
		return LLMConfig{}, errors.New("sessiontitle: automaticMode must be first-prompt or all-prompts")
	}
	if settings.TargetWords <= 0 {
		return LLMConfig{}, errors.New("sessiontitle: targetWords must be a positive integer")
	}
	if settings.TargetCJKCharacters <= 0 {
		return LLMConfig{}, errors.New("sessiontitle: targetCjkCharacters must be a positive integer")
	}
	if settings.MaxInputBytes <= 0 {
		return LLMConfig{}, errors.New("sessiontitle: maxInputBytes must be a positive integer")
	}
	if settings.MaxOutputTokens <= 0 {
		return LLMConfig{}, errors.New("sessiontitle: maxOutputTokens must be a positive integer")
	}
	if settings.TimeoutMS <= 0 {
		return LLMConfig{}, errors.New("sessiontitle: timeoutMs must be a positive integer")
	}
	maximumTimerMilliseconds := int64((1<<63 - 1) / time.Millisecond)
	if settings.TimeoutMS > maximumTimerMilliseconds {
		return LLMConfig{}, fmt.Errorf("sessiontitle: timeoutMs must not exceed %d", maximumTimerMilliseconds)
	}
	if strings.TrimSpace(settings.Provider) == "" || strings.TrimSpace(settings.Model) == "" {
		return LLMConfig{}, errors.New("sessiontitle: title provider and model are required")
	}

	return settings, nil
}

// LLMRequestEventData records the exact model-visible auxiliary request before
// dispatch. It is deliberately excluded from the Session surface.
type LLMRequestEventData struct {
	TitleProvider ProviderID             `json:"titleProvider"`
	MessageSeqs   []int64                `json:"messageSeqs"`
	Route         ModelProvenance        `json:"route"`
	System        string                 `json:"system"`
	Messages      []agentmessage.Message `json:"messages"`
	MaxTokens     int                    `json:"maxTokens"`
}

// TitleLLMRequest is the log-only pre-dispatch title request event.
var TitleLLMRequest = session.DefineEvent[LLMRequestEventData](TitleLLMRequestEventName)

// LLMProvider implements either source title cadence while sharing one
// request policy and model boundary.
type LLMProvider struct {
	identifier ProviderID
	mode       AutomaticMode
	runtime    LLMStreamer
	settings   LLMConfig
}

// NewFirstPromptLLMProvider creates the Base-profile provider that selects the
// first eligible human message.
func NewFirstPromptLLMProvider(runtime LLMStreamer, settings LLMConfig) (*LLMProvider, error) {
	settings.AutomaticMode = AutomaticFirstPrompt
	return newLLMProvider(FirstPromptLLMProviderID, AutomaticFirstPrompt, runtime, settings)
}

// NewAllPromptsLLMProvider creates the provider that selects every eligible
// human message in the service revision.
func NewAllPromptsLLMProvider(runtime LLMStreamer, settings LLMConfig) (*LLMProvider, error) {
	settings.AutomaticMode = AutomaticAllPrompts
	return newLLMProvider(AllPromptsLLMProviderID, AutomaticAllPrompts, runtime, settings)
}

// NewConfiguredLLMProvider creates the title feature selected by its typed
// automaticMode policy.
func NewConfiguredLLMProvider(runtime LLMStreamer, settings LLMConfig) (*LLMProvider, error) {
	validated, err := settings.Validate()
	if err != nil {
		return nil, err
	}
	switch validated.AutomaticMode {
	case AutomaticFirstPrompt:
		return newLLMProvider(FirstPromptLLMProviderID, AutomaticFirstPrompt, runtime, validated)
	case AutomaticAllPrompts:
		return newLLMProvider(AllPromptsLLMProviderID, AutomaticAllPrompts, runtime, validated)
	default:
		return nil, errors.New("sessiontitle: automaticMode is invalid")
	}
}

func newLLMProvider(
	identifier ProviderID,
	mode AutomaticMode,
	runtime LLMStreamer,
	settings LLMConfig,
) (*LLMProvider, error) {
	if runtime == nil {
		return nil, errors.New("sessiontitle: LLM runtime is required")
	}
	validated, err := settings.Validate()
	if err != nil {
		return nil, err
	}
	if validated.AutomaticMode != mode {
		return nil, errors.New("sessiontitle: configured automaticMode does not match provider mode")
	}
	return &LLMProvider{
		identifier: identifier,
		mode:       mode,
		runtime:    runtime,
		settings:   validated,
	}, nil
}

func (implementation *LLMProvider) ID() ProviderID { return implementation.identifier }

func (implementation *LLMProvider) AutomaticMode() AutomaticMode { return implementation.mode }

// Prepare frames and snapshots the auxiliary call without I/O.
func (implementation *LLMProvider) Prepare(request ProviderRequest) (Generation, error) {
	if request.SessionID == "" {
		return nil, errors.New("sessiontitle: SessionID is required")
	}
	selected, err := implementation.selectMessages(request.Messages)
	if err != nil {
		return nil, err
	}
	framedInput, err := frameTitleMessages(selected)
	if err != nil {
		return nil, err
	}
	if len([]byte(framedInput)) > implementation.settings.MaxInputBytes {
		return nil, fmt.Errorf(
			"sessiontitle: input is %d bytes, exceeding maxInputBytes %d",
			len([]byte(framedInput)), implementation.settings.MaxInputBytes,
		)
	}
	route := ModelProvenance{
		Provider: implementation.settings.Provider,
		Model:    implementation.settings.Model,
	}
	messageValue, err := agentmessage.NewUserMessage(
		agentmessage.UserMessageInput{
			Content: []agentmessage.ContentBlock{agentmessage.NewTextBlock(framedInput)},
			Source:  agentmessage.PluginMessageSource{Kind: "plugin", Plugin: "dsh-session-title-llm"},
		},
	)
	if err != nil {
		return nil, err
	}
	systemInstruction := implementation.systemPrompt()
	messageSeqs := selectedMessageSeqs(selected)
	maxOutputTokens := implementation.settings.MaxOutputTokens
	callOptions := llm.GenerateOptions{
		CallConfig: llm.CallConfig{
			Provider:  route.Provider,
			Model:     route.Model,
			MaxTokens: &maxOutputTokens,
		},
		Messages:  []agentmessage.Message{messageValue},
		System:    &systemInstruction,
		SessionID: string(request.SessionID), Purpose: llm.PurposeSessionTitle,
	}
	requestDraft, err := session.NewEventDraft(
		TitleLLMRequest,
		LLMRequestEventData{
			TitleProvider: implementation.identifier,
			MessageSeqs:   append([]int64{}, messageSeqs...),
			Route:         route, System: systemInstruction,
			Messages: []agentmessage.Message{messageValue}, MaxTokens: maxOutputTokens,
		},
	)
	if err != nil {
		return nil, err
	}
	return &generation{
		runtime:     implementation.runtime,
		settings:    implementation.settings,
		options:     callOptions,
		draft:       requestDraft,
		messageSeqs: messageSeqs,
		route:       route,
	}, nil
}

type generation struct {
	runtime     LLMStreamer
	settings    LLMConfig
	options     llm.GenerateOptions
	draft       session.EventDraft
	messageSeqs []int64
	route       ModelProvenance
}

func (call *generation) Draft() session.EventDraft { return call.draft }

func (call *generation) Generate(requestContext context.Context) (ProviderResult, error) {
	if requestContext == nil {
		return ProviderResult{}, errors.New("sessiontitle: generation context is nil")
	}
	if err := requestContext.Err(); err != nil {
		return ProviderResult{}, err
	}

	deadline := time.Duration(call.settings.TimeoutMS) * time.Millisecond
	callContext, cancelCall := context.WithTimeoutCause(
		requestContext,
		deadline,
		&titleLLMTimeoutError{duration: deadline},
	)
	defer cancelCall()
	flow, err := call.runtime.Stream(callContext, call.options)
	if err != nil {
		return ProviderResult{}, err
	}
	if flow == nil {
		return ProviderResult{}, errors.New("sessiontitle: LLM runtime returned a nil stream")
	}
	defer func() { _ = flow.Close(context.Background()) }()
	assembly := llm.NewBlockAssembler()
	for {
		if err := callContext.Err(); err != nil {
			return ProviderResult{}, context.Cause(callContext)
		}
		entry, available, nextErr := flow.Next(callContext)
		if nextErr != nil {
			if callContext.Err() != nil {
				return ProviderResult{}, context.Cause(callContext)
			}
			return ProviderResult{}, nextErr
		}
		if !available {
			break
		}
		if err := assembly.Push(entry); err != nil {
			return ProviderResult{}, err
		}
	}
	if err := callContext.Err(); err != nil {
		return ProviderResult{}, context.Cause(callContext)
	}
	if err := validateTitleFinish(assembly.FinishValue()); err != nil {
		return ProviderResult{}, err
	}
	blocks, err := assembly.AssembledBlocks()
	if err != nil {
		return ProviderResult{}, err
	}
	visible := make([]string, 0, len(blocks))
	for _, blockValue := range blocks {
		switch typedBlock := blockValue.(type) {
		case agentmessage.TextBlock:
			visible = append(visible, typedBlock.Text)
		case *agentmessage.TextBlock:
			visible = append(visible, typedBlock.Text)
		case agentmessage.ToolCallBlock, *agentmessage.ToolCallBlock:
			return ProviderResult{}, errors.New("sessiontitle: title output must contain text only")
		}
	}
	titleValue, err := NormalizeSessionTitle(strings.Join(visible, " "), int(^uint(0)>>1))
	if err != nil {
		return ProviderResult{}, err
	}
	if titleValue == "" {
		return ProviderResult{}, errors.New("sessiontitle: title model produced no text")
	}
	return ProviderResult{Title: titleValue, MessageSeqs: append([]int64(nil), call.messageSeqs...), Model: cloneRoute(&call.route)}, nil
}

func (implementation *LLMProvider) selectMessages(messages []UserMessage) ([]UserMessage, error) {
	if len(messages) == 0 {
		return nil, errors.New("sessiontitle: at least one source message is required")
	}
	if implementation.mode == AutomaticFirstPrompt {
		return []UserMessage{messages[0]}, nil
	}
	return cloneMessages(messages), nil
}

func (implementation *LLMProvider) systemPrompt() string {
	return strings.Join([]string{
		"Create a concise title for an AI coding-assistant session from the supplied human messages.",
		"Return only the title on one line, **in plain text of natural language**, with no quotes, prefix, explanation, Markdown, XML, or terminal control codes. No code is allowed.",
		"Use the language of the messages.",
		fmt.Sprintf(
			"Aim for about %d words in non-CJK languages or %d CJK characters.",
			implementation.settings.TargetWords,
			implementation.settings.TargetCJKCharacters,
		),
	}, "\n")
}

func frameTitleMessages(messages []UserMessage) (string, error) {
	rawValue, err := json.Marshal(messages)
	if err != nil {
		return "", fmt.Errorf("sessiontitle: frame source messages: %w", err)
	}
	return "Generate the session title from this JSON array of human messages:\n" + string(rawValue), nil
}

func selectedMessageSeqs(messages []UserMessage) []int64 {
	result := make([]int64, len(messages))
	for index, messageValue := range messages {
		result[index] = messageValue.Seq
	}
	return result
}

func validateTitleFinish(reason llm.FinishReason) error {
	switch reason.ReasonKind() {
	case "stop":
		return nil
	case "max-tokens":
		return errors.New("sessiontitle: title output reached maxOutputTokens")
	case "tool-calls":
		return errors.New("sessiontitle: title model unexpectedly requested a tool")
	case "error":
		if terminal, ok := reason.(llm.ErrorFinish); ok {
			return errors.New(terminal.Failure.Message)
		}
		if terminal, ok := reason.(*llm.ErrorFinish); ok {
			return errors.New(terminal.Failure.Message)
		}
	case "aborted":
		if terminal, ok := reason.(llm.AbortedFinish); ok {
			return errors.New(terminal.Failure.Message)
		}
		if terminal, ok := reason.(*llm.AbortedFinish); ok {
			return errors.New(terminal.Failure.Message)
		}
	}
	return fmt.Errorf("sessiontitle: unsupported finish reason %q", reason.ReasonKind())
}

type titleLLMTimeoutError struct {
	duration time.Duration
}

func (problem *titleLLMTimeoutError) Error() string {
	return fmt.Sprintf("sessiontitle: title request exceeded %s", problem.duration)
}

func (*titleLLMTimeoutError) Code() string { return TitleLLMTimeoutCode }
