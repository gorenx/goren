package tools

import (
	"context"
)

// ExecuteAction is the Tool-owned terminal contract for one body dispatch.
type ExecuteAction interface {
	Execute(context.Context, ExecuteRequest) (ExecuteOutcome, error)
}

// ExecuteMiddleware wraps Tool body dispatch for one tool call.
type ExecuteMiddleware interface {
	InterceptExecute(
		context.Context,
		ExecuteRequest,
		ExecuteAction,
	) (ExecuteOutcome, error)
}
