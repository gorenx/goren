package tools

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/internal/jsonvalue"
)

// ToolExecutionSuccess is a validated canonical value and its projections.
type ToolExecutionSuccess struct {
	Value              json.RawMessage
	Content            []agentmessage.ContentBlock
	Meta               json.RawMessage
	AdditionalContexts []agentmessage.UserMessage
	// ConcludesTurn means this Tool success ends the current Agent Turn; Agent
	// Loop must not automatically start another model request for its result.
	ConcludesTurn bool
}

// Failed reports false.
func (*ToolExecutionSuccess) Failed() bool { return false }

// ContentBlocks returns the result's model-facing content.
func (outcome *ToolExecutionSuccess) ContentBlocks() []agentmessage.ContentBlock {
	if outcome == nil {
		return nil
	}
	detached, _ := agentmessage.CloneContentBlocks(outcome.Content)
	return detached
}

// AdditionalContextMessages returns detached next-step context in authored order.
func (outcome *ToolExecutionSuccess) AdditionalContextMessages() []agentmessage.UserMessage {
	if outcome == nil {
		return nil
	}
	detached, _ := cloneUserMessages(outcome.AdditionalContexts)
	return detached
}

// SuccessValue returns a detached canonical value.
func (outcome *ToolExecutionSuccess) SuccessValue() (json.RawMessage, bool) {
	if outcome == nil {
		return nil, false
	}
	return append(json.RawMessage(nil), outcome.Value...), true
}

// FailureDetail reports that a successful result has no failure.
func (*ToolExecutionSuccess) FailureDetail() (ToolFailure, bool) {
	return ToolFailure{}, false
}

// PresentationMeta returns detached tool-private presentation metadata.
func (outcome *ToolExecutionSuccess) PresentationMeta() json.RawMessage {
	if outcome == nil {
		return nil
	}
	return append(json.RawMessage(nil), outcome.Meta...)
}

// ConcludesAgentTurn reports whether this success terminates the current turn.
func (outcome *ToolExecutionSuccess) ConcludesAgentTurn() bool {
	return outcome != nil && outcome.ConcludesTurn
}

func (outcome *ToolExecutionSuccess) cloneResult() (ToolExecutionResult, error) {
	if outcome == nil {
		return nil, errors.New("tools: nil success result")
	}
	value, err := jsonvalue.Clone(outcome.Value)
	if err != nil {
		return nil, fmt.Errorf("tools: invalid successful value: %w", err)
	}
	content, err := agentmessage.CloneContentBlocks(outcome.Content)
	if err != nil {
		return nil, err
	}
	meta, err := cloneOptionalJSON(outcome.Meta)
	if err != nil {
		return nil, fmt.Errorf("tools: invalid presentation metadata: %w", err)
	}
	additionalContexts, err := cloneUserMessages(outcome.AdditionalContexts)
	if err != nil {
		return nil, fmt.Errorf("tools: invalid additional context: %w", err)
	}
	return &ToolExecutionSuccess{
		Value:              value,
		Content:            content,
		Meta:               meta,
		AdditionalContexts: additionalContexts,
		ConcludesTurn:      outcome.ConcludesTurn,
	}, nil
}

func (outcome *ToolExecutionFailure) cloneResult() (ToolExecutionResult, error) {
	if outcome == nil {
		return nil, errors.New("tools: nil failure result")
	}
	if outcome.Error.Message == "" {
		return nil, errors.New("tools: failure result message is empty")
	}
	content, err := agentmessage.CloneContentBlocks(outcome.Content)
	if err != nil {
		return nil, err
	}
	meta, err := cloneOptionalJSON(outcome.Meta)
	if err != nil {
		return nil, fmt.Errorf("tools: invalid presentation metadata: %w", err)
	}
	retainedError := outcome.Error
	if outcome.Error.Info != nil {
		retainedInfo := *outcome.Error.Info
		retainedError.Info = &retainedInfo
	}
	additionalContexts, err := cloneUserMessages(outcome.AdditionalContexts)
	if err != nil {
		return nil, fmt.Errorf("tools: invalid additional context: %w", err)
	}
	return &ToolExecutionFailure{
		Error:              retainedError,
		Content:            content,
		Meta:               meta,
		AdditionalContexts: additionalContexts,
	}, nil
}

func cloneOptionalJSON(rawValue json.RawMessage) (json.RawMessage, error) {
	if len(rawValue) == 0 {
		return nil, nil
	}
	return jsonvalue.Clone(rawValue)
}

type resultSnapshot struct {
	failed             bool
	content            []agentmessage.ContentBlock
	value              json.RawMessage
	failure            ToolFailure
	hasFailure         bool
	meta               json.RawMessage
	additionalContexts []agentmessage.UserMessage
	concludesTurn      bool
}

// SnapshotResult validates a result and returns an immutable observer view.
func SnapshotResult(outcome ToolExecutionResult) (ToolResultSnapshot, error) {
	detached, err := outcome.cloneResult()
	if err != nil {
		return nil, err
	}
	resultView := &resultSnapshot{
		failed: detached.Failed(),
	}
	switch retained := detached.(type) {
	case *ToolExecutionSuccess:
		resultView.content = retained.Content
		resultView.value = retained.Value
		resultView.meta = retained.Meta
		resultView.additionalContexts = retained.AdditionalContexts
		resultView.concludesTurn = retained.ConcludesTurn
	case *ToolExecutionFailure:
		resultView.content = retained.Content
		resultView.failure = retained.Error
		resultView.hasFailure = true
		resultView.meta = retained.Meta
		resultView.additionalContexts = retained.AdditionalContexts
	default:
		return nil, errors.New("tools: unsupported result implementation")
	}
	return resultView, nil
}

func (resultView *resultSnapshot) Failed() bool { return resultView.failed }

func (resultView *resultSnapshot) ContentBlocks() []agentmessage.ContentBlock {
	detached, _ := agentmessage.CloneContentBlocks(resultView.content)
	return detached
}

func (resultView *resultSnapshot) SuccessValue() (json.RawMessage, bool) {
	if resultView.failed {
		return nil, false
	}
	return append(json.RawMessage(nil), resultView.value...), true
}

func (resultView *resultSnapshot) FailureDetail() (ToolFailure, bool) {
	if !resultView.hasFailure {
		return ToolFailure{}, false
	}
	detail := resultView.failure
	if resultView.failure.Info != nil {
		retainedInfo := *resultView.failure.Info
		detail.Info = &retainedInfo
	}
	return detail, true
}

func (resultView *resultSnapshot) PresentationMeta() json.RawMessage {
	return append(json.RawMessage(nil), resultView.meta...)
}

func (resultView *resultSnapshot) AdditionalContextMessages() []agentmessage.UserMessage {
	detached, _ := cloneUserMessages(resultView.additionalContexts)
	return detached
}

func (resultView *resultSnapshot) ConcludesAgentTurn() bool { return resultView.concludesTurn }

func cloneUserMessages(source []agentmessage.UserMessage) ([]agentmessage.UserMessage, error) {
	if source == nil {
		return nil, nil
	}
	detached := make([]agentmessage.UserMessage, len(source))
	for index, message := range source {
		copyValue, err := agentmessage.CloneUserMessage(message)
		if err != nil {
			return nil, fmt.Errorf("message %d: %w", index, err)
		}
		detached[index] = copyValue
	}
	return detached, nil
}

// CloneResult validates and detaches a result crossing the execution boundary.
func CloneResult(outcome ToolExecutionResult) (ToolExecutionResult, error) {
	if outcome == nil {
		return nil, errors.New("tools: nil result")
	}
	return outcome.cloneResult()
}
