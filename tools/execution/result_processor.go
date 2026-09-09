package execution

import (
	"context"
	"errors"
	"fmt"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/tools"
)

// resultProcessor owns post-execute policy, definition finalization, and the
// immutable final result Event.
type resultProcessor struct {
	dispatcher *dispatcher
}

func (processor *resultProcessor) post(
	requestContext context.Context,
	toolCall callExecution,
	entry *tools.Contract,
	outcome tools.ToolExecutionResult,
) (tools.ToolExecutionResult, error) {
	if outcome == nil {
		return nil, errors.New("tools: staged result is nil")
	}
	detached, err := tools.CloneResult(outcome)
	if err != nil {
		return nil, err
	}
	resultView, err := tools.SnapshotResult(detached)
	if err != nil {
		return nil, err
	}
	postOutcome, err := toolCall.state.pipeline.ReviewResult(
		requestContext,
		tools.NewPostExecuteRequest(cloneExecution(toolCall), resultView),
	)
	if err != nil {
		return nil, err
	}
	return processor.applyPostDecision(
		toolCall,
		entry,
		detached,
		postOutcome.Decision,
	)
}

func (processor *resultProcessor) applyPostDecision(
	toolCall callExecution,
	entry *tools.Contract,
	outcome tools.ToolExecutionResult,
	decision tools.PostToolDecision,
) (tools.ToolExecutionResult, error) {
	switch selected := decision.(type) {
	case tools.AcceptDecision:
		return appendAdditionalContexts(
			outcome,
			selected.AdditionalContexts,
		)
	case tools.ReplaceContentDecision:
		content, err := agentmessage.CloneContentBlocks(selected.Content)
		if err != nil {
			return nil, err
		}
		return appendAdditionalContexts(
			replaceResultContent(outcome, content),
			selected.AdditionalContexts,
		)
	case tools.ReplaceValueDecision:
		if outcome.Failed() {
			return nil, errors.New(
				"tools: post-execute cannot replace the value of a failed result",
			)
		}
		if entry == nil {
			return nil, &tools.ToolNotFoundError{
				Name: toolCall.Name,
			}
		}
		replaced, err := processor.dispatcher.success(
			toolCall,
			entry,
			selected.Value,
		)
		if err != nil {
			return nil, err
		}
		combined := append(
			outcome.AdditionalContextMessages(),
			selected.AdditionalContexts...,
		)
		return appendAdditionalContexts(replaced, combined)
	case tools.BlockDecision:
		content, err := agentmessage.CloneContentBlocks(selected.Feedback)
		if err != nil {
			return nil, err
		}
		blocked := &tools.ToolExecutionFailure{
			Error: tools.ToolFailure{
				Message: failureMessage(content),
			},
			Content: content,
		}
		return appendAdditionalContexts(
			blocked,
			selected.AdditionalContexts,
		)
	case nil:
		return nil, errors.New(
			"tools: post-execute returned a nil decision",
		)
	default:
		return nil, fmt.Errorf(
			"tools: unsupported post-execute decision %T",
			decision,
		)
	}
}

func (processor *resultProcessor) finish(
	requestContext context.Context,
	toolCall callExecution,
	outcome tools.ToolExecutionResult,
	finalizer tools.ContentFinalizer,
) tools.ToolExecutionResult {
	var finalOutcome tools.ToolExecutionResult
	if outcome == nil {
		finalOutcome = errorResult(errors.New("tools: staged result is nil"))
	} else {
		var err error
		finalOutcome, err = tools.CloneResult(outcome)
		if err != nil {
			finalOutcome = errorResult(err)
		}
	}
	if finalizer != nil {
		content, replaceContent, finalizeErr := invokeFinalizer(
			finalizer,
			toolCall,
			finalOutcome,
		)
		if finalizeErr != nil {
			finalOutcome = errorResult(finalizeErr)
		} else if replaceContent {
			content, finalizeErr = agentmessage.CloneContentBlocks(content)
			if finalizeErr != nil {
				finalOutcome = errorResult(finalizeErr)
			} else {
				finalOutcome = replaceResultContent(finalOutcome, content)
			}
		}
	}
	materialized, err := tools.CloneResult(finalOutcome)
	if err != nil {
		materialized = errorResult(err)
	}
	resultView, err := tools.SnapshotResult(materialized)
	if err != nil {
		materialized = errorResult(err)
		resultView, _ = tools.SnapshotResult(materialized)
	}
	_ = toolCall.state.pipeline.PublishCompleted(
		requestContext,
		tools.NewExecutionCompleted(cloneExecution(toolCall), resultView),
	)
	returned, err := tools.CloneResult(materialized)
	if err != nil {
		return errorResult(err)
	}
	return returned
}

func invokeFinalizer(
	finalizer tools.ContentFinalizer,
	toolCall callExecution,
	outcome tools.ToolExecutionResult,
) (content []agentmessage.ContentBlock, replaceContent bool, invokeErr error) {
	defer func() {
		panicValue := recover()
		if panicValue != nil {
			invokeErr = fmt.Errorf(
				"tools: content finalizer panicked: %v",
				panicValue,
			)
		}
	}()
	resultView, err := tools.SnapshotResult(outcome)
	if err != nil {
		return nil, false, err
	}
	content, replaceContent = finalizer.Finalize(
		cloneExecution(toolCall),
		resultView,
	)
	return content, replaceContent, nil
}
