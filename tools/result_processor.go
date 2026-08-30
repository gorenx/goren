package tools

import (
	"context"
	"errors"
	"fmt"

	"github.com/gorenx/goren/agentmessage"
)

// resultProcessor owns post-execute policy, definition finalization, and the
// immutable final result Event.
type resultProcessor struct {
	effects    layerEffects
	dispatcher *dispatcher
}

func (processor *resultProcessor) post(
	requestContext context.Context,
	toolCall ToolExecution,
	entry *registeredTool,
	outcome ToolExecutionResult,
) (ToolExecutionResult, error) {
	if outcome == nil {
		return nil, errors.New("tools: staged result is nil")
	}
	detached, err := outcome.cloneResult()
	if err != nil {
		return nil, err
	}
	resultView, err := newResultSnapshot(detached)
	if err != nil {
		return nil, err
	}
	postOutcome, err := processor.effects.ResolvePostExecute(
		requestContext,
		PostExecuteRequest{
			toolCall:   toolCall,
			resultView: resultView,
		},
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
	toolCall ToolExecution,
	entry *registeredTool,
	outcome ToolExecutionResult,
	decision PostToolDecision,
) (ToolExecutionResult, error) {
	switch selected := decision.(type) {
	case AcceptDecision:
		return appendAdditionalContexts(
			outcome,
			selected.AdditionalContexts,
		)
	case ReplaceContentDecision:
		content, err := agentmessage.CloneContentBlocks(selected.Content)
		if err != nil {
			return nil, err
		}
		return appendAdditionalContexts(
			replaceResultContent(outcome, content),
			selected.AdditionalContexts,
		)
	case ReplaceValueDecision:
		if outcome.Failed() {
			return nil, errors.New(
				"tools: post-execute cannot replace the value of a failed result",
			)
		}
		if entry == nil {
			return nil, &ToolNotFoundError{
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
	case BlockDecision:
		content, err := agentmessage.CloneContentBlocks(selected.Feedback)
		if err != nil {
			return nil, err
		}
		blocked := &ToolExecutionFailure{
			Error: ToolFailure{
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

type postExecuteTerminal struct{}

func (postExecuteTerminal) Execute(
	context.Context,
	PostExecuteRequest,
) (PostExecuteOutcome, error) {
	return PostExecuteOutcome{
		Decision: AcceptDecision{},
	}, nil
}

func (processor *resultProcessor) finish(
	requestContext context.Context,
	toolCall ToolExecution,
	outcome ToolExecutionResult,
	finalizer ContentFinalizer,
) ToolExecutionResult {
	var finalOutcome ToolExecutionResult
	if outcome == nil {
		finalOutcome = errorResult(errors.New("tools: staged result is nil"))
	} else {
		var err error
		finalOutcome, err = outcome.cloneResult()
		if err != nil {
			finalOutcome = errorResult(err)
		}
	}
	if finalizer != nil {
		content, replace, finalizeErr := invokeFinalizer(
			finalizer,
			toolCall,
			finalOutcome,
		)
		if finalizeErr != nil {
			finalOutcome = errorResult(finalizeErr)
		} else if replace {
			content, finalizeErr = agentmessage.CloneContentBlocks(content)
			if finalizeErr != nil {
				finalOutcome = errorResult(finalizeErr)
			} else {
				finalOutcome = replaceResultContent(finalOutcome, content)
			}
		}
	}
	materialized, err := finalOutcome.cloneResult()
	if err != nil {
		materialized = errorResult(err)
	}
	resultView, err := newResultSnapshot(materialized)
	if err != nil {
		materialized = errorResult(err)
		resultView, _ = newResultSnapshot(materialized)
	}
	_ = processor.effects.PublishCompleted(
		requestContext,
		ExecutionCompleted{
			toolCall:   cloneExecution(toolCall),
			resultView: resultView,
		},
	)
	returned, err := materialized.cloneResult()
	if err != nil {
		return errorResult(err)
	}
	return returned
}

func invokeFinalizer(
	finalizer ContentFinalizer,
	toolCall ToolExecution,
	outcome ToolExecutionResult,
) (content []agentmessage.ContentBlock, replace bool, invokeErr error) {
	defer func() {
		if panicValue := recover(); panicValue != nil {
			invokeErr = fmt.Errorf(
				"tools: content finalizer panicked: %v",
				panicValue,
			)
		}
	}()
	resultView, err := newResultSnapshot(outcome)
	if err != nil {
		return nil, false, err
	}
	content, replace = finalizer.Finalize(
		cloneExecution(toolCall),
		resultView,
	)
	return content, replace, nil
}
