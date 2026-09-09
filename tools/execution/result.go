package execution

import (
	"errors"
	"strings"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/tools"
)

type canonicalSuccess struct {
	*tools.ToolExecutionSuccess
	execution *scheduledExecutionState
}

func errorResult(cause error) *tools.ToolExecutionFailure {
	message := "unknown tool failure"
	if cause != nil {
		message = cause.Error()
	}
	var coded tools.CodedError
	var info *tools.ToolErrorInfo
	if errors.As(cause, &coded) {
		info = &tools.ToolErrorInfo{
			Name: coded.ToolErrorName(),
			Code: coded.ToolErrorCode(),
		}
	}
	return &tools.ToolExecutionFailure{
		Error: tools.ToolFailure{
			Message: message,
			Info:    info,
		},
		Content: []agentmessage.ContentBlock{agentmessage.NewTextBlock("Error: " + message)},
	}
}

func abortedResult(bodyInvoked bool, prior ...tools.ToolExecutionResult) *tools.ToolExecutionFailure {
	var additionalContexts []agentmessage.UserMessage
	if len(prior) != 0 && prior[0] != nil {
		additionalContexts = prior[0].AdditionalContextMessages()
	}
	var outcome *tools.ToolExecutionFailure
	if bodyInvoked {
		outcome = errorResult(&abortError{
			message: "tool call aborted",
			code:    tools.ToolAborted,
		})
	} else {
		outcome = errorResult(&abortError{
			message: "tool call aborted before dispatch",
			code:    tools.ToolAbortedBeforeDispatch,
		})
	}
	outcome.AdditionalContexts = additionalContexts
	return outcome
}

func failureMessage(content []agentmessage.ContentBlock) string {
	parts := make([]string, 0, len(content))
	for _, entry := range content {
		readable, supported := entry.(agentmessage.PlainTextContent)
		if supported {
			textValue, available := readable.PlainText()
			if available {
				parts = append(parts, textValue)
				continue
			}
		}
		parts = append(parts, "["+entry.ContentType()+" content]")
	}
	message := strings.Join(parts, "\n")
	if message == "" {
		return "tool result blocked by post-execute policy"
	}
	return message
}

func replaceResultContent(outcome tools.ToolExecutionResult, content []agentmessage.ContentBlock) tools.ToolExecutionResult {
	switch retained := outcome.(type) {
	case *tools.ToolExecutionSuccess:
		return &tools.ToolExecutionSuccess{
			Value:              retained.Value,
			Content:            content,
			Meta:               retained.Meta,
			AdditionalContexts: retained.AdditionalContexts,
			ConcludesTurn:      retained.ConcludesTurn,
		}
	case *tools.ToolExecutionFailure:
		return &tools.ToolExecutionFailure{
			Error:              retained.Error,
			Content:            content,
			Meta:               retained.Meta,
			AdditionalContexts: retained.AdditionalContexts,
		}
	default:
		return errorResult(errors.New("tools: unsupported result implementation"))
	}
}

func appendAdditionalContexts(
	outcome tools.ToolExecutionResult,
	additions []agentmessage.UserMessage,
) (tools.ToolExecutionResult, error) {
	detached, err := tools.CloneResult(outcome)
	if err != nil {
		return nil, err
	}
	additionalContexts := additions
	switch retained := detached.(type) {
	case *tools.ToolExecutionSuccess:
		retained.AdditionalContexts = append(retained.AdditionalContexts, additionalContexts...)
	case *tools.ToolExecutionFailure:
		retained.AdditionalContexts = append(retained.AdditionalContexts, additionalContexts...)
	default:
		return nil, errors.New("tools: unsupported result implementation")
	}
	return tools.CloneResult(detached)
}

func prependAdditionalContexts(
	outcome tools.ToolExecutionResult,
	additions []agentmessage.UserMessage,
) (tools.ToolExecutionResult, error) {
	detached, err := tools.CloneResult(outcome)
	if err != nil {
		return nil, err
	}
	additionalContexts := additions
	switch retained := detached.(type) {
	case *tools.ToolExecutionSuccess:
		retained.AdditionalContexts = append(
			additionalContexts,
			retained.AdditionalContexts...,
		)
	case *tools.ToolExecutionFailure:
		retained.AdditionalContexts = append(
			additionalContexts,
			retained.AdditionalContexts...,
		)
	default:
		return nil, errors.New("tools: unsupported result implementation")
	}
	return tools.CloneResult(detached)
}

type abortError struct {
	message string
	code    string
}

func (failure *abortError) Error() string         { return failure.message }
func (*abortError) ToolErrorName() string         { return "AbortError" }
func (failure *abortError) ToolErrorCode() string { return failure.code }
