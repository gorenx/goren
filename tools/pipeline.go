package tools

import (
	"context"
	"errors"
	"fmt"
)

// pipeline is an immutable membership snapshot used by one prepared execution.
// Implementations referenced by its interfaces own their own mutable state.
type pipeline struct {
	options    Options
	observers  []ResultObserver
	executions []ExecuteMiddleware
}

func (stages *pipeline) ResolvePreExecute(ctx context.Context, request PreExecuteRequest) (outcome PreExecuteOutcome, failure error) {
	defer recoverPipelineFailure(&failure, "pre-execute policy")
	if stages.options.Before == nil {
		return PreExecuteOutcome{
			Decision: AllowDecision{},
		}, nil
	}
	return stages.options.Before.ResolvePreExecute(ctx, request)
}

func (stages *pipeline) ResolvePostExecute(ctx context.Context, request PostExecuteRequest) (outcome PostExecuteOutcome, failure error) {
	defer recoverPipelineFailure(&failure, "post-execute policy")
	if stages.options.After == nil {
		return PostExecuteOutcome{
			Decision: AcceptDecision{},
		}, nil
	}
	return stages.options.After.ResolvePostExecute(ctx, request)
}

func (stages *pipeline) ResolveExecute(ctx context.Context, request ExecuteRequest, terminal ExecuteAction) (outcome ExecuteOutcome, failure error) {
	defer recoverPipelineFailure(&failure, "execution middleware")
	chain := terminal
	for index := len(stages.executions) - 1; index >= 0; index-- {
		chain = executeMiddlewareAction{
			middleware: stages.executions[index],
			next:       chain,
		}
	}
	if stages.options.Execution != nil {
		return stages.options.Execution.InterceptExecute(ctx, request, chain)
	}
	return chain.Execute(ctx, request)
}

func (stages *pipeline) PublishCompleted(ctx context.Context, completed ExecutionCompleted) (failure error) {
	defer recoverPipelineFailure(&failure, "result observer")
	for _, observer := range stages.observers {
		failure = errors.Join(failure, observer.ObserveToolResult(ctx, completed))
	}
	if stages.options.Results != nil {
		failure = errors.Join(failure, stages.options.Results.ObserveToolResult(ctx, completed))
	}
	return failure
}

func (stages *pipeline) PublishChanged(ctx context.Context) (failure error) {
	defer recoverPipelineFailure(&failure, "registry observer")
	if stages.options.Changes == nil {
		return nil
	}
	return stages.options.Changes.RegistryChanged(ctx)
}

func recoverPipelineFailure(failure *error, operation string) {
	if recovered := recover(); recovered != nil {
		*failure = fmt.Errorf("tools: %s panicked: %v", operation, recovered)
	}
}
