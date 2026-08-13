package openai

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gorenx/goren/llm"
	"github.com/openai/openai-go/v3/responses"
)

type responsesBlock struct {
	kind      string
	itemID    string
	callID    string
	name      string
	signature string
	text      strings.Builder
	arguments strings.Builder
	closed    bool
}

type responsesState struct {
	model         llm.Model
	emitter       llm.StreamEmitter
	blocks        []*responsesBlock
	items         map[string]*responsesBlock
	responseID    string
	responseModel string
	usage         llm.Usage
	stopReason    llm.StopReason
	finishError   string
	hasFinished   bool
}

func newResponsesState(targetModel llm.Model, eventSink llm.StreamEmitter) *responsesState {
	return &responsesState{
		model:      targetModel,
		emitter:    eventSink,
		items:      make(map[string]*responsesBlock),
		stopReason: llm.StopReasonStop,
	}
}

func (responseAssembler *responsesState) consume(streamEvent responses.ResponseStreamEventUnion) error {
	switch streamEvent.Type {
	case "response.created":
		responseAssembler.captureIdentity(streamEvent.Response)
	case "response.output_item.added":
		return responseAssembler.startItem(streamEvent.Item)
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		return responseAssembler.appendThinking(streamEvent.ItemID, streamEvent.Delta)
	case "response.reasoning_summary_part.done":
		if contentBlock := responseAssembler.items[streamEvent.ItemID]; contentBlock != nil && contentBlock.kind == "thinking" {
			contentBlock.text.WriteString("\n\n")
			responseAssembler.emitter.Emit(llm.ThinkingDeltaEvent{
				ContentIndex: responseAssembler.blockIndex(contentBlock),
				Delta:        "\n\n",
			})
		}
	case "response.output_text.delta", "response.refusal.delta":
		return responseAssembler.appendText(streamEvent.ItemID, streamEvent.Delta)
	case "response.function_call_arguments.delta":
		return responseAssembler.appendToolArguments(streamEvent.ItemID, streamEvent.Delta)
	case "response.function_call_arguments.done":
		return responseAssembler.finishToolArguments(streamEvent.ItemID, streamEvent.Arguments)
	case "response.output_item.done":
		return responseAssembler.finishItem(streamEvent.Item)
	case "response.completed":
		return responseAssembler.captureCompletion(streamEvent.Response)
	case "response.incomplete":
		responseAssembler.captureIdentity(streamEvent.Response)
		responseAssembler.captureUsage(streamEvent.Response)
		responseAssembler.hasFinished = true
		if streamEvent.Response.IncompleteDetails.Reason == "max_output_tokens" {
			responseAssembler.stopReason = llm.StopReasonLength
			return nil
		}
		responseAssembler.stopReason = llm.StopReasonError
		responseAssembler.finishError = "provider incomplete reason: " + streamEvent.Response.IncompleteDetails.Reason
		if streamEvent.Response.IncompleteDetails.Reason == "" {
			responseAssembler.finishError = "OpenAI response incomplete without a reason"
		}
	case "response.failed":
		return responseFailure(streamEvent.Response)
	case "error":
		if streamEvent.Code != "" {
			return fmt.Errorf("OpenAI response error %s: %s", streamEvent.Code, streamEvent.Message)
		}
		return errors.New(streamEvent.Message)
	}
	return nil
}

func (responseAssembler *responsesState) startItem(item responses.ResponseOutputItemUnion) error {
	if item.ID == "" {
		return fmt.Errorf("OpenAI response output item %q has no ID", item.Type)
	}
	if responseAssembler.items[item.ID] != nil {
		return fmt.Errorf("OpenAI response output item %q was added twice", item.ID)
	}

	contentBlock := &responsesBlock{kind: item.Type, itemID: item.ID}
	switch item.Type {
	case "reasoning":
		contentBlock.kind = "thinking"
		responseAssembler.appendBlock(contentBlock)
		responseAssembler.emitter.Emit(llm.ThinkingStartEvent{ContentIndex: responseAssembler.blockIndex(contentBlock)})
	case "message":
		contentBlock.kind = "text"
		responseAssembler.appendBlock(contentBlock)
		responseAssembler.emitter.Emit(llm.TextStartEvent{ContentIndex: responseAssembler.blockIndex(contentBlock)})
	case "function_call":
		contentBlock.kind = "tool"
		contentBlock.callID = item.CallID
		contentBlock.name = item.Name
		contentBlock.arguments.WriteString(item.Arguments.OfString)
		responseAssembler.appendBlock(contentBlock)
		responseAssembler.emitter.Emit(llm.ToolCallStartEvent{
			ContentIndex: responseAssembler.blockIndex(contentBlock),
			ID:           responsesToolCallID(contentBlock.callID, contentBlock.itemID),
			Name:         contentBlock.name,
		})
	default:
		return nil
	}
	responseAssembler.items[item.ID] = contentBlock
	return nil
}

func (responseAssembler *responsesState) appendBlock(contentBlock *responsesBlock) {
	responseAssembler.blocks = append(responseAssembler.blocks, contentBlock)
}

func (responseAssembler *responsesState) appendThinking(itemID string, delta string) error {
	contentBlock, err := responseAssembler.openBlock(itemID, "thinking")
	if err != nil {
		return err
	}
	contentBlock.text.WriteString(delta)
	responseAssembler.emitter.Emit(llm.ThinkingDeltaEvent{
		ContentIndex: responseAssembler.blockIndex(contentBlock),
		Delta:        delta,
	})
	return nil
}

func (responseAssembler *responsesState) appendText(itemID string, delta string) error {
	contentBlock, err := responseAssembler.openBlock(itemID, "text")
	if err != nil {
		return err
	}
	contentBlock.text.WriteString(delta)
	responseAssembler.emitter.Emit(llm.TextDeltaEvent{
		ContentIndex: responseAssembler.blockIndex(contentBlock),
		Delta:        delta,
	})
	return nil
}

func (responseAssembler *responsesState) appendToolArguments(itemID string, delta string) error {
	contentBlock, err := responseAssembler.openBlock(itemID, "tool")
	if err != nil {
		return err
	}
	contentBlock.arguments.WriteString(delta)
	responseAssembler.emitter.Emit(llm.ToolCallDeltaEvent{
		ContentIndex: responseAssembler.blockIndex(contentBlock),
		Delta:        delta,
	})
	return nil
}

func (responseAssembler *responsesState) finishToolArguments(itemID string, arguments string) error {
	contentBlock, err := responseAssembler.openBlock(itemID, "tool")
	if err != nil {
		return err
	}
	partial := contentBlock.arguments.String()
	if strings.HasPrefix(arguments, partial) {
		delta := strings.TrimPrefix(arguments, partial)
		if delta != "" {
			contentBlock.arguments.WriteString(delta)
			responseAssembler.emitter.Emit(llm.ToolCallDeltaEvent{
				ContentIndex: responseAssembler.blockIndex(contentBlock),
				Delta:        delta,
			})
		}
		return nil
	}
	contentBlock.arguments.Reset()
	contentBlock.arguments.WriteString(arguments)
	return nil
}

func (responseAssembler *responsesState) openBlock(itemID string, expectedKind string) (*responsesBlock, error) {
	contentBlock := responseAssembler.items[itemID]
	if contentBlock == nil {
		return nil, fmt.Errorf("OpenAI response event references unknown item %q", itemID)
	}
	if contentBlock.kind != expectedKind {
		return nil, fmt.Errorf("OpenAI response item %q is %q, want %q", itemID, contentBlock.kind, expectedKind)
	}
	if contentBlock.closed {
		return nil, fmt.Errorf("OpenAI response item %q received data after completion", itemID)
	}
	return contentBlock, nil
}

func (responseAssembler *responsesState) finishItem(item responses.ResponseOutputItemUnion) error {
	contentBlock := responseAssembler.items[item.ID]
	if contentBlock == nil {
		if err := responseAssembler.startItem(item); err != nil {
			return err
		}
		contentBlock = responseAssembler.items[item.ID]
	}
	if contentBlock == nil {
		return nil
	}
	if contentBlock.closed {
		return fmt.Errorf("OpenAI response output item %q completed twice", item.ID)
	}

	index := responseAssembler.blockIndex(contentBlock)
	switch contentBlock.kind {
	case "thinking":
		finalText := reasoningText(item)
		if finalText != "" {
			contentBlock.text.Reset()
			contentBlock.text.WriteString(finalText)
		}
		contentBlock.signature = item.RawJSON()
		responseAssembler.emitter.Emit(llm.ThinkingEndEvent{ContentIndex: index, Content: contentBlock.text.String()})
	case "text":
		finalText := outputMessageText(item)
		if finalText != "" {
			contentBlock.text.Reset()
			contentBlock.text.WriteString(finalText)
		}
		responseAssembler.emitter.Emit(llm.TextEndEvent{ContentIndex: index, Content: contentBlock.text.String()})
	case "tool":
		if item.CallID != "" {
			contentBlock.callID = item.CallID
		}
		if item.Name != "" {
			contentBlock.name = item.Name
		}
		if contentBlock.arguments.Len() == 0 && item.Arguments.OfString != "" {
			contentBlock.arguments.WriteString(item.Arguments.OfString)
		}
		arguments := contentBlock.arguments.String()
		if arguments == "" {
			arguments = `{}`
		}
		if !json.Valid([]byte(arguments)) {
			return fmt.Errorf("tool call %q has invalid streamed arguments", contentBlock.name)
		}
		responseAssembler.emitter.Emit(llm.ToolCallEndEvent{
			ContentIndex: index,
			ToolCall: llm.ToolCall{
				ID:        responsesToolCallID(contentBlock.callID, contentBlock.itemID),
				Name:      contentBlock.name,
				Arguments: json.RawMessage(arguments),
			},
		})
	}
	contentBlock.closed = true
	return nil
}

func (responseAssembler *responsesState) captureCompletion(response responses.Response) error {
	responseAssembler.captureIdentity(response)
	responseAssembler.captureUsage(response)
	responseAssembler.hasFinished = true
	switch response.Status {
	case "", responses.ResponseStatusCompleted:
		responseAssembler.stopReason = llm.StopReasonStop
	case responses.ResponseStatusIncomplete:
		if response.IncompleteDetails.Reason == "max_output_tokens" {
			responseAssembler.stopReason = llm.StopReasonLength
		} else {
			responseAssembler.stopReason = llm.StopReasonError
			responseAssembler.finishError = "provider incomplete reason: " + response.IncompleteDetails.Reason
		}
	case responses.ResponseStatusFailed, responses.ResponseStatusCancelled:
		return responseFailure(response)
	default:
		return fmt.Errorf("OpenAI response completed with status %q", response.Status)
	}
	return nil
}

func (responseAssembler *responsesState) captureIdentity(response responses.Response) {
	if response.ID != "" {
		responseAssembler.responseID = response.ID
	}
	if response.Model != "" {
		responseAssembler.responseModel = string(response.Model)
	}
}

func (responseAssembler *responsesState) captureUsage(response responses.Response) {
	cacheRead := int(response.Usage.InputTokensDetails.CachedTokens)
	cacheWrite := int(response.Usage.InputTokensDetails.CacheWriteTokens)
	responseAssembler.usage = llm.Usage{
		InputTokens:      max(0, int(response.Usage.InputTokens)-cacheRead-cacheWrite),
		OutputTokens:     int(response.Usage.OutputTokens),
		CacheReadTokens:  cacheRead,
		CacheWriteTokens: cacheWrite,
		TotalTokens:      int(response.Usage.TotalTokens),
	}
	if responseAssembler.usage.TotalTokens == 0 {
		responseAssembler.usage.TotalTokens = responseAssembler.usage.InputTokens +
			responseAssembler.usage.OutputTokens + cacheRead + cacheWrite
	}
}

func (responseAssembler *responsesState) finish() (llm.AssistantMessage, error) {
	assistantReply := llm.AssistantMessage{
		API:           responseAssembler.model.API,
		Provider:      responseAssembler.model.Provider,
		Model:         responseAssembler.model.ID,
		ResponseModel: responseAssembler.responseModel,
		ResponseID:    responseAssembler.responseID,
		Usage:         responseAssembler.usage,
		StopReason:    responseAssembler.stopReason,
		Timestamp:     time.Now(),
	}
	assistantReply.Usage.Cost = responseAssembler.model.CalculateCost(assistantReply.Usage)
	for _, contentBlock := range responseAssembler.blocks {
		switch contentBlock.kind {
		case "text":
			assistantReply.Content = append(assistantReply.Content, llm.TextContent{Text: contentBlock.text.String()})
		case "thinking":
			assistantReply.Content = append(assistantReply.Content, llm.ThinkingContent{
				Thinking:  contentBlock.text.String(),
				Signature: contentBlock.signature,
			})
		case "tool":
			arguments := contentBlock.arguments.String()
			if arguments == "" {
				arguments = `{}`
			}
			assistantReply.Content = append(assistantReply.Content, llm.ToolCall{
				ID:        responsesToolCallID(contentBlock.callID, contentBlock.itemID),
				Name:      contentBlock.name,
				Arguments: json.RawMessage(arguments),
			})
		}
		if !contentBlock.closed {
			return assistantReply, fmt.Errorf("OpenAI response item %q did not complete", contentBlock.itemID)
		}
	}
	if !responseAssembler.hasFinished {
		return assistantReply, errors.New("LLM stream ended without response completion")
	}
	if responseAssembler.finishError != "" {
		return assistantReply, errors.New(responseAssembler.finishError)
	}
	if responseAssembler.stopReason == llm.StopReasonStop {
		for _, contentBlock := range responseAssembler.blocks {
			if contentBlock.kind == "tool" {
				assistantReply.StopReason = llm.StopReasonToolUse
				break
			}
		}
	}
	return assistantReply, nil
}

func (responseAssembler *responsesState) blockIndex(contentBlock *responsesBlock) int {
	for index, candidate := range responseAssembler.blocks {
		if candidate == contentBlock {
			return index
		}
	}
	return -1
}

func reasoningText(item responses.ResponseOutputItemUnion) string {
	var parts []string
	for _, summary := range item.Summary {
		if summary.Text != "" {
			parts = append(parts, summary.Text)
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, "\n\n")
	}
	reasoning := item.AsReasoning()
	for _, content := range reasoning.Content {
		if content.Text != "" {
			parts = append(parts, content.Text)
		}
	}
	return strings.Join(parts, "\n\n")
}

func outputMessageText(item responses.ResponseOutputItemUnion) string {
	var visibleText strings.Builder
	for _, content := range item.Content {
		switch content.Type {
		case "output_text":
			visibleText.WriteString(content.Text)
		case "refusal":
			visibleText.WriteString(content.Refusal)
		}
	}
	return visibleText.String()
}

func responseFailure(response responses.Response) error {
	if response.Error.Code != "" || response.Error.Message != "" {
		return fmt.Errorf("OpenAI response failed (%s): %s", response.Error.Code, response.Error.Message)
	}
	if response.IncompleteDetails.Reason != "" {
		return fmt.Errorf("OpenAI response failed: %s", response.IncompleteDetails.Reason)
	}
	return errors.New("OpenAI response failed without error details")
}

func responsesToolCallID(callID string, itemID string) string {
	if itemID == "" {
		return callID
	}
	return callID + "|" + itemID
}
