package execution

import (
	"context"
	"errors"
	"fmt"

	"github.com/gorenx/goren/tools"
)

// pipeline is an immutable membership snapshot used by one prepared execution.
// Implementations referenced by its interfaces own their own mutable state.
type pipeline struct {
	options Policies
}

func (stages *pipeline) EvaluateCall(ctx context.Context, request tools.PreExecuteRequest) (outcome tools.PreExecuteOutcome, failure error) {
	defer recoverPipelineFailure(&failure, "pre-execute policy")
	if stages.options.Before == nil {
		return tools.PreExecuteOutcome{
			Decision: tools.AllowDecision{},
		}, nil
	}
	return stages.options.Before.EvaluateCall(ctx, request)
}

func (stages *pipeline) ReviewResult(ctx context.Context, request tools.PostExecuteRequest) (outcome tools.PostExecuteOutcome, failure error) {
	defer recoverPipelineFailure(&failure, "post-execute policy")
	if stages.options.After == nil {
		return tools.PostExecuteOutcome{
			Decision: tools.AcceptDecision{},
		}, nil
	}
	return stages.options.After.ReviewResult(ctx, request)
}

func (stages *pipeline) ExecuteBody(ctx context.Context, request tools.ExecuteRequest, terminal tools.ExecuteAction) (outcome tools.ExecuteOutcome, failure error) {
	defer recoverPipelineFailure(&failure, "execution middleware")
	if stages.options.Execution != nil {
		return stages.options.Execution.InterceptExecute(ctx, request, terminal)
	}
	return terminal.Execute(ctx, request)
}

func (stages *pipeline) PublishCompleted(ctx context.Context, completed tools.ExecutionCompleted) (failure error) {
	defer recoverPipelineFailure(&failure, "result observer")
	if stages.options.Results != nil {
		failure = errors.Join(failure, stages.options.Results.ObserveToolResult(ctx, completed))
	}
	return failure
}

func recoverPipelineFailure(failure *error, operation string) {
	recovered := recover()
	if recovered != nil {
		*failure = fmt.Errorf("tools: %s panicked: %v", operation, recovered)
	}
}
