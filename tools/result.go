package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/gorenx/goren/internal/jsonvalue"
	"github.com/gorenx/goren/llm"
)

func (outcome *ToolExecutionSuccess) cloneResult() (ToolExecutionResult, error) {
	if outcome == nil {
		return nil, errors.New("tools: nil success result")
	}
	value, err := jsonvalue.Clone(outcome.Value)
	if err != nil {
		return nil, fmt.Errorf("tools: invalid successful value: %w", err)
	}
	content, err := llm.CloneContentBlocks(outcome.Content)
	if err != nil {
		return nil, err
	}
	meta, err := cloneOptionalJSON(outcome.Meta)
	if err != nil {
		return nil, fmt.Errorf("tools: invalid presentation metadata: %w", err)
	}
	return &ToolExecutionSuccess{
		Value: value, Content: content, Meta: meta, ConcludesTurn: outcome.ConcludesTurn, owner: outcome.owner,
	}, nil
}

func (outcome *ToolExecutionFailure) cloneResult() (ToolExecutionResult, error) {
	if outcome == nil {
		return nil, errors.New("tools: nil failure result")
	}
	if outcome.Error.Message == "" {
		return nil, errors.New("tools: failure result message is empty")
	}
	content, err := llm.CloneContentBlocks(outcome.Content)
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
	return &ToolExecutionFailure{Error: retainedError, Content: content, Meta: meta}, nil
}

func cloneOptionalJSON(rawValue json.RawMessage) (json.RawMessage, error) {
	if len(rawValue) == 0 {
		return nil, nil
	}
	return jsonvalue.Clone(rawValue)
}

func errorResult(cause error) *ToolExecutionFailure {
	message := "unknown tool failure"
	if cause != nil {
		message = cause.Error()
	}
	var coded CodedError
	var info *ToolErrorInfo
	if errors.As(cause, &coded) {
		info = &ToolErrorInfo{Name: coded.ToolErrorName(), Code: coded.ToolErrorCode()}
	}
	return &ToolExecutionFailure{
		Error:   ToolFailure{Message: message, Info: info},
		Content: []llm.ContentBlock{llm.NewTextBlock("Error: " + message)},
	}
}

func abortedResult(bodyInvoked bool) *ToolExecutionFailure {
	if bodyInvoked {
		return errorResult(&abortError{message: "tool call aborted", code: ToolAborted})
	}
	return errorResult(&abortError{message: "tool call aborted before dispatch", code: ToolAbortedBeforeDispatch})
}

func failureMessage(content []llm.ContentBlock) string {
	parts := make([]string, 0, len(content))
	for _, entry := range content {
		if textEntry, ok := entry.(llm.TextBlock); ok {
			parts = append(parts, textEntry.Text)
			continue
		}
		parts = append(parts, "["+entry.ContentType()+" content]")
	}
	message := strings.Join(parts, "\n")
	if message == "" {
		return "tool result blocked by post-execute policy"
	}
	return message
}

func replaceResultContent(outcome ToolExecutionResult, content []llm.ContentBlock) ToolExecutionResult {
	switch retained := outcome.(type) {
	case *ToolExecutionSuccess:
		return &ToolExecutionSuccess{
			Value: retained.Value, Content: content, Meta: retained.Meta,
			ConcludesTurn: retained.ConcludesTurn, owner: retained.owner,
		}
	case *ToolExecutionFailure:
		return &ToolExecutionFailure{Error: retained.Error, Content: content, Meta: retained.Meta}
	default:
		return errorResult(errors.New("tools: unsupported result implementation"))
	}
}

type resultSnapshot struct {
	failed        bool
	content       []llm.ContentBlock
	value         json.RawMessage
	failure       ToolFailure
	hasFailure    bool
	meta          json.RawMessage
	concludesTurn bool
}

func newResultSnapshot(outcome ToolExecutionResult) (ToolResultSnapshot, error) {
	detached, err := outcome.cloneResult()
	if err != nil {
		return nil, err
	}
	snapshot := &resultSnapshot{failed: detached.Failed()}
	switch retained := detached.(type) {
	case *ToolExecutionSuccess:
		snapshot.content = retained.Content
		snapshot.value = retained.Value
		snapshot.meta = retained.Meta
		snapshot.concludesTurn = retained.ConcludesTurn
	case *ToolExecutionFailure:
		snapshot.content = retained.Content
		snapshot.failure = retained.Error
		snapshot.hasFailure = true
		snapshot.meta = retained.Meta
	default:
		return nil, errors.New("tools: unsupported result implementation")
	}
	return snapshot, nil
}

func (snapshot *resultSnapshot) Failed() bool { return snapshot.failed }

func (snapshot *resultSnapshot) ContentBlocks() []llm.ContentBlock {
	detached, _ := llm.CloneContentBlocks(snapshot.content)
	return detached
}

func (snapshot *resultSnapshot) SuccessValue() (json.RawMessage, bool) {
	if snapshot.failed {
		return nil, false
	}
	return append(json.RawMessage(nil), snapshot.value...), true
}

func (snapshot *resultSnapshot) FailureDetail() (ToolFailure, bool) {
	if !snapshot.hasFailure {
		return ToolFailure{}, false
	}
	detail := snapshot.failure
	if snapshot.failure.Info != nil {
		retainedInfo := *snapshot.failure.Info
		detail.Info = &retainedInfo
	}
	return detail, true
}

func (snapshot *resultSnapshot) PresentationMeta() json.RawMessage {
	return append(json.RawMessage(nil), snapshot.meta...)
}

func (snapshot *resultSnapshot) ConcludesAgentTurn() bool { return snapshot.concludesTurn }
