package basic

import (
	"errors"
	"fmt"
	"strings"

	"github.com/gorenx/goren/agentmessage"
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
	messages []agentmessage.Message
}

// Summary contains detached model output accepted as checkpoint text.
type Summary struct {
	Content []agentmessage.ContentBlock
	Output  []agentmessage.ContentBlock
	Usage   *llm.TokenUsage
}

// Summarizer owns one summary's policy and output assembly, without I/O resources.
type Summarizer struct {
	catalog  *policyCatalog
	assembly *llm.BlockAssembler
}

// NewSummarizer captures the policy for one summary generation.
func NewSummarizer(settings ResolvedConfig) *Summarizer {
	return &Summarizer{
		catalog:  newPolicyCatalog(settings),
		assembly: llm.NewBlockAssembler(),
	}
}

func buildSummarizationInput(
	snapshot session.Snapshot,
	shadowedSeqs []int64,
) (summarizationInput, error) {
	entries := snapshot.Events
	headerValue, err := session.LatestRequestHeader(entries)
	if err != nil {
		return summarizationInput{}, err
	}
	messages := make([]agentmessage.Message, 0, len(shadowedSeqs))
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
	detachedMessages, err := agentmessage.CloneMessages(messages)
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

// Request prepares selected history and routing without invoking the LLM.
func (summarizer *Summarizer) Request(
	snapshot session.Snapshot,
	selectedSeqs []int64,
	fallback RouteTarget,
) (llm.GenerateOptions, error) {
	input, err := buildSummarizationInput(snapshot, selectedSeqs)
	if err != nil {
		return llm.GenerateOptions{}, err
	}
	conversationTarget, targetFound, err := summarizationConversationTarget(snapshot, fallback)
	if err != nil {
		return llm.GenerateOptions{}, err
	}
	targetPolicy := summarizer.catalog.defaults()
	if targetFound {
		targetPolicy = summarizer.catalog.resolve(conversationTarget)
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
		return llm.GenerateOptions{}, errors.New(
			"no provider/model available for summarization: set both BasicCompactionConfig summarization fields, route one request, or set both Agent Context fields",
		)
	}
	instruction, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
		Content: []agentmessage.ContentBlock{
			agentmessage.NewTextBlock(compactionInstruction),
		},
		Source: agentmessage.PluginMessageSource{
			Plugin: "dsh-compaction-basic",
		},
	})
	if err != nil {
		return llm.GenerateOptions{}, err
	}
	messages, err := agentmessage.CloneMessages(input.messages)
	if err != nil {
		return llm.GenerateOptions{}, err
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
		SessionID: string(snapshot.Barrier.SessionID),
		Purpose:   llm.PurposeCompaction,
	}
	return llm.CloneGenerateOptions(callOptions)
}

// Push consumes one delivered model chunk. The caller owns stream ordering and closure.
func (summarizer *Summarizer) Push(chunk llm.StreamChunk) error {
	return summarizer.assembly.Push(chunk)
}

// Result projects safe non-empty text while retaining raw output and usage.
// Successful projection does not imply that the caller has closed its LLM resources.
func (summarizer *Summarizer) Result() (Summary, error) {
	assembly := summarizer.assembly
	if err := validateSummaryFinish(assembly.FinishValue()); err != nil {
		return Summary{}, err
	}
	rawOutput, err := assembly.AssembledBlocks()
	if err != nil {
		return Summary{}, err
	}
	summaryBlocks, err := summaryText(rawOutput)
	if err != nil {
		return Summary{}, err
	}
	nonEmpty := false
	for _, blockValue := range summaryBlocks {
		textValue, textual := blockValue.(agentmessage.PlainTextContent)
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
		return Summary{}, errors.New(
			"summarization produced no text summary content",
		)
	}
	var usageValue *llm.TokenUsage
	if observedUsage, present := assembly.UsageValue(); present {
		usageValue = &observedUsage
	}
	return Summary{
		Content: summaryBlocks,
		Output:  rawOutput,
		Usage:   usageValue,
	}, nil
}

func summarizationConversationTarget(snapshot session.Snapshot, fallback RouteTarget) (RouteTarget, bool, error) {
	header, err := session.LatestRequestHeader(snapshot.Events)
	if err != nil {
		return RouteTarget{}, false, err
	}
	if header != nil && header.Config.Provider != "" && header.Config.Model != "" {
		return RouteTarget{
			Provider: header.Config.Provider,
			Model:    header.Config.Model,
		}, true, nil
	}
	if fallback.Provider == "" && fallback.Model == "" {
		return RouteTarget{}, false, nil
	}
	if fallback.Provider == "" || fallback.Model == "" {
		return RouteTarget{}, false, errors.New("compaction-basic: default provider and model must be set together")
	}
	return fallback, true, nil
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
	blocks []agentmessage.ContentBlock,
) ([]agentmessage.ContentBlock, error) {
	if agentmessage.ContentHasImage(blocks) {
		return nil, llm.MustLlmError(
			"compaction summary cannot contain image output",
			"UNSUPPORTED_CONTENT",
		)
	}
	textBlocks := make([]agentmessage.ContentBlock, 0, len(blocks))
	for _, blockValue := range blocks {
		if blockValue != nil && blockValue.ContentType() == "text" {
			textBlocks = append(textBlocks, blockValue)
		}
	}
	return agentmessage.CloneContentBlocks(textBlocks)
}

func frameSummary(
	summaryBlocks []agentmessage.ContentBlock,
) ([]agentmessage.ContentBlock, error) {
	framed := make([]agentmessage.ContentBlock, 0, len(summaryBlocks)+2)
	framed = append(
		framed,
		agentmessage.NewTextBlock(checkpointPreamble+"\n\n"+summaryOpenTag),
	)
	cloned, err := agentmessage.CloneContentBlocks(summaryBlocks)
	if err != nil {
		return nil, err
	}
	framed = append(framed, cloned...)
	framed = append(framed, agentmessage.NewTextBlock(summaryCloseTag))
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

func cloneUsage(source *llm.TokenUsage) *llm.TokenUsage {
	if source == nil {
		return nil
	}
	detached := *source
	detached.CacheReadTokens = cloneInt64(source.CacheReadTokens)
	detached.CacheWriteTokens = cloneInt64(source.CacheWriteTokens)
	detached.ReasoningTokens = cloneInt64(source.ReasoningTokens)
	return &detached
}
