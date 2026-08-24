package basic

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/gorenx/goren/compaction"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

const summaryOpenTag = "<compacted-summary>"
const summaryCloseTag = "</compacted-summary>"

const checkpointPreamble = "This is an automatically generated checkpoint condensing an earlier span of the conversation to free up context. Treat the captured context as established background and build on it without restating it. Continue the task directly from the messages that follow, without acknowledging this checkpoint."

var compactionInstruction = strings.Join([]string{
	"You are now acting as a compaction engine for this AI coding assistant. Condense the conversation ABOVE into a structured checkpoint that lets another model resume the work with no loss of essential context.",
	"",
	"Output EXACTLY the Markdown structure below: keep every section, in order. Use terse bullets, not prose paragraphs. Write \"(none)\" for an empty section — never drop a section.",
	"",
	"## Primary Request and Intent",
	"- [the user's original and evolving goals; quote verbatim where the exact wording matters]",
	"",
	"## Key Technical Concepts",
	"- [technologies, frameworks, patterns, and conventions in play]",
	"",
	"## Files and Code",
	"- [exact path: why it matters, key changes or snippets]",
	"",
	"## Errors and Fixes",
	"- [error: how it was resolved, plus any related user feedback]",
	"",
	"## Pending Jobs",
	"- [explicitly requested work not yet completed]",
	"",
	"## Current Work",
	"- [precisely what was in progress at this checkpoint]",
	"",
	"## Next Step",
	"- [the single next action, directly in line with the most recent request, or \"(none)\"]",
	"",
	"## Critical Context",
	"- [decisions and their rationale, constraints, user preferences, open questions, data needed to continue]",
	"",
	"Rules:",
	"- Write concise English engineering prose. Preserve exact file paths, commands, error strings, identifiers, numeric values, function signatures, and syntax fragments.",
	"- Capture user feedback and explicit instructions faithfully, especially corrections.",
	"- Do NOT mention this summarization request or that the context was compacted.",
	"- Output only the checkpoint text: do not call any tool or take any other action.",
	"- If the conversation already contains a " + summaryOpenTag + " block, it is a PRIOR checkpoint. Do not copy it forward verbatim: preserve still-true facts, drop stale ones, and merge newer information into a single consolidated summary under the same structure.",
}, "\n")

type summarizationInput struct {
	system   *string
	tools    []llm.ToolSchema
	messages []llm.Message
}

type summaryResult struct {
	summary       []llm.ContentBlock
	rawOutput     []llm.ContentBlock
	llmStreamCall bool
	provider      string
	model         string
	maxTokens     *int
	usage         *llm.TokenUsage
}

// llmSummarizer owns the auxiliary LLM protocol, route selection, stream
// assembly, and safe text projection. It does not mutate the Session.
type llmSummarizer struct {
	policy     ResolvedConfig
	llmRuntime llm.LlmRuntime
}

func newLLMSummarizer(settings ResolvedConfig) llmSummarizer {
	return llmSummarizer{
		policy: cloneResolvedConfig(settings),
	}
}

func (summarizer *llmSummarizer) bind(llmRuntime llm.LlmRuntime) {
	summarizer.llmRuntime = llmRuntime
}

func (summarizer *llmSummarizer) release() {
	summarizer.llmRuntime = nil
}

func buildSummarizationInput(
	conversation session.Context,
	shadowedSeqs []int64,
) (summarizationInput, error) {
	entries := conversation.Events()
	headerValue, err := session.LatestRequestHeader(entries)
	if err != nil {
		return summarizationInput{}, err
	}
	messages := make([]llm.Message, 0, len(shadowedSeqs))
	for _, sequence := range shadowedSeqs {
		if sequence < 0 || sequence >= int64(len(entries)) ||
			entries[sequence].Seq != sequence {
			return summarizationInput{}, fmt.Errorf(
				"compaction-basic: Surface seq %d has no matching Event",
				sequence,
			)
		}
		messageValue, deriveErr := session.DeriveEventMessage(entries[sequence])
		if deriveErr != nil {
			return summarizationInput{}, deriveErr
		}
		if messageValue != nil {
			messages = append(messages, messageValue)
		}
	}
	detachedMessages, err := llm.CloneMessages(messages)
	if err != nil {
		return summarizationInput{}, err
	}
	input := summarizationInput{
		messages: detachedMessages,
	}
	if headerValue != nil {
		if headerValue.System != nil {
			promptValue := *headerValue.System
			input.system = &promptValue
		}
		input.tools = cloneToolSchemas(headerValue.Tools)
	}
	return input, nil
}

func (summarizer *llmSummarizer) summarize(
	requestContext context.Context,
	input summarizationInput,
	ownerContext compaction.AgentContext,
) (summaryResult, error) {
	conversationTarget, targetFound, err := summarizationConversationTarget(
		ownerContext,
	)
	if err != nil {
		return summaryResult{}, err
	}
	targetPolicy := ResolvedTargetPolicy{
		SummarizationProvider: summarizer.policy.SummarizationProvider,
		SummarizationModel:    summarizer.policy.SummarizationModel,
		MaxTokens:             summarizer.policy.MaxTokens,
	}
	if targetFound {
		targetPolicy = ResolveTargetPolicy(
			summarizer.policy,
			conversationTarget,
		)
	}
	selectedTarget := RouteTarget{}
	if targetPolicy.SummarizationProvider != "" {
		selectedTarget = RouteTarget{
			Provider: targetPolicy.SummarizationProvider,
			Model:    targetPolicy.SummarizationModel,
		}
	} else if targetFound {
		selectedTarget = conversationTarget
	}
	if selectedTarget.Provider == "" || selectedTarget.Model == "" {
		return summaryResult{}, errors.New(
			"no provider/model available for summarization: set both BasicCompactionConfig summarization fields, route one request, or set both Agent Context fields",
		)
	}
	instruction, err := llm.NewUserMessage(llm.UserMessageInput{
		Content: []llm.ContentBlock{
			llm.NewTextBlock(compactionInstruction),
		},
		Source: llm.PluginMessageSource{
			Plugin: "dsh-compaction-basic",
		},
	})
	if err != nil {
		return summaryResult{}, err
	}
	messages, err := llm.CloneMessages(input.messages)
	if err != nil {
		return summaryResult{}, err
	}
	messages = append(messages, instruction)
	maxTokens := targetPolicy.MaxTokens
	callOptions := llm.GenerateOptions{
		CallConfig: llm.CallConfig{
			Provider:  selectedTarget.Provider,
			Model:     selectedTarget.Model,
			MaxTokens: &maxTokens,
		},
		Messages:  messages,
		System:    cloneString(input.system),
		Tools:     cloneToolSchemas(input.tools),
		SessionID: string(ownerContext.Session.ID()),
		Purpose:   llm.PurposeCompaction,
	}
	flow, err := summarizer.llmRuntime.Stream(requestContext, callOptions)
	if err != nil {
		return summaryResult{}, err
	}
	if flow == nil {
		return summaryResult{}, errors.New(
			"compaction-basic: LLM Runtime returned a nil summary stream",
		)
	}
	defer func() {
		_ = flow.Close(context.Background())
	}()
	assembly := llm.NewBlockAssembler()
	for {
		if err := contextFailure(requestContext); err != nil {
			return summaryResult{}, err
		}
		chunk, available, nextErr := flow.Next(requestContext)
		if nextErr != nil {
			if contextErr := contextFailure(requestContext); contextErr != nil {
				return summaryResult{}, contextErr
			}
			return summaryResult{}, nextErr
		}
		if !available {
			break
		}
		if err := assembly.Push(chunk); err != nil {
			return summaryResult{}, err
		}
	}
	if err := validateSummaryFinish(assembly.FinishValue()); err != nil {
		return summaryResult{}, err
	}
	rawOutput, err := assembly.AssembledBlocks()
	if err != nil {
		return summaryResult{}, err
	}
	summaryBlocks, err := summaryText(rawOutput)
	if err != nil {
		return summaryResult{}, err
	}
	nonEmpty := false
	for _, blockValue := range summaryBlocks {
		textValue, textual := blockValue.(llm.PlainTextContent)
		if !textual {
			continue
		}
		plainText, present := textValue.PlainText()
		if present && strings.TrimSpace(plainText) != "" {
			nonEmpty = true
			break
		}
	}
	if !nonEmpty {
		return summaryResult{}, errors.New(
			"summarization produced no text summary content",
		)
	}
	var usageValue *llm.TokenUsage
	if observedUsage, present := assembly.UsageValue(); present {
		usageValue = cloneUsage(&observedUsage)
	}
	return summaryResult{
		summary:       summaryBlocks,
		rawOutput:     rawOutput,
		llmStreamCall: true,
		provider:      selectedTarget.Provider,
		model:         selectedTarget.Model,
		maxTokens:     &maxTokens,
		usage:         usageValue,
	}, nil
}

func summarizationConversationTarget(
	ownerContext compaction.AgentContext,
) (RouteTarget, bool, error) {
	selectedTarget, found, err := routedTarget(ownerContext.Session)
	if err != nil || found {
		return selectedTarget, found, err
	}
	if ownerContext.Provider == "" && ownerContext.Model == "" {
		return RouteTarget{}, false, nil
	}
	if ownerContext.Provider == "" || ownerContext.Model == "" {
		return RouteTarget{}, false, errors.New(
			"compaction-basic: Agent Context provider and model must be set together",
		)
	}
	return RouteTarget{
		Provider: ownerContext.Provider,
		Model:    ownerContext.Model,
	}, true, nil
}

func validateSummaryFinish(reason llm.FinishReason) error {
	switch selected := reason.(type) {
	case llm.ErrorFinish:
		return summaryFailure(selected.Failure)
	case *llm.ErrorFinish:
		return summaryFailure(selected.Failure)
	case llm.AbortedFinish:
		return summaryFailure(selected.Failure)
	case *llm.AbortedFinish:
		return summaryFailure(selected.Failure)
	case llm.MaxTokensFinish, *llm.MaxTokensFinish:
		return llm.MustLlmError(
			"summarization truncated at the token cap (incomplete checkpoint)",
			"MAX_TOKENS",
		)
	default:
		return nil
	}
}

func summaryFailure(failure llm.LlmFailure) error {
	problem, err := llm.NewLlmError(
		failure.Message,
		failure.Code,
		llm.LlmErrorOptions{
			Status:               failure.Status,
			ProviderRetryAfterMS: failure.ProviderRetryAfterMS,
			RequestID:            failure.RequestID,
		},
	)
	if err != nil {
		return fmt.Errorf("compaction-basic: invalid summary failure: %w", err)
	}
	return problem
}

func summaryText(
	blocks []llm.ContentBlock,
) ([]llm.ContentBlock, error) {
	if llm.ContentHasImage(blocks) {
		return nil, llm.MustLlmError(
			"compaction summary cannot contain image output",
			"UNSUPPORTED_CONTENT",
		)
	}
	textBlocks := make([]llm.ContentBlock, 0, len(blocks))
	for _, blockValue := range blocks {
		if blockValue != nil && blockValue.ContentType() == "text" {
			textBlocks = append(textBlocks, blockValue)
		}
	}
	return llm.CloneContentBlocks(textBlocks)
}

func frameSummary(
	summaryBlocks []llm.ContentBlock,
) ([]llm.ContentBlock, error) {
	framed := make([]llm.ContentBlock, 0, len(summaryBlocks)+2)
	framed = append(
		framed,
		llm.NewTextBlock(checkpointPreamble+"\n\n"+summaryOpenTag),
	)
	cloned, err := llm.CloneContentBlocks(summaryBlocks)
	if err != nil {
		return nil, err
	}
	framed = append(framed, cloned...)
	framed = append(framed, llm.NewTextBlock(summaryCloseTag))
	return framed, nil
}

func cloneToolSchemas(source []llm.ToolSchema) []llm.ToolSchema {
	if source == nil {
		return nil
	}
	detached := make([]llm.ToolSchema, len(source))
	for schemaIndex, schemaValue := range source {
		detached[schemaIndex] = schemaValue
		detached[schemaIndex].Parameters = append(
			[]byte(nil),
			schemaValue.Parameters...,
		)
	}
	return detached
}
