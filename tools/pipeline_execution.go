package tools

import (
	"context"
)

// ExecuteAction is the Tool-owned terminal contract for one body dispatch.
type ExecuteAction interface {
	Execute(context.Context, ExecuteRequest) (ExecuteOutcome, error)
}

// ExecuteMiddleware wraps Tool body dispatch inside one exact Tool ToolLayer.
type ExecuteMiddleware interface {
	InterceptExecute(
		context.Context,
		ExecuteRequest,
		ExecuteAction,
	) (ExecuteOutcome, error)
}

type executeMiddlewareAction struct {
	middleware ExecuteMiddleware
	next       ExecuteAction
}

func (action executeMiddlewareAction) Execute(
	requestContext context.Context,
	request ExecuteRequest,
) (ExecuteOutcome, error) {
	return action.middleware.InterceptExecute(
		requestContext,
		request,
		action.next,
	)
}
