package approval

import (
	"context"
	"errors"
	"fmt"

	"github.com/gorenx/goren/plugin"
)

// RequestNext delegates to the remaining approval answerer chain.
type RequestNext func(context.Context) (Outcome, error)

// RequestHandler may answer one request or delegate it to the next answerer.
type RequestHandler func(context.Context, Request, RequestNext) (Outcome, error)

var requestEvent = plugin.DefineEvent[Request, Outcome](RequestEventName, plugin.ModeWaterfall)

// OnRequest registers one scope-owned approval answerer.
func OnRequest(ownerScope *plugin.Scope, callback RequestHandler) (plugin.Disposer, error) {
	if callback == nil {
		return nil, errors.New("approval: request handler is nil")
	}
	return plugin.OnWaterfall(ownerScope, requestEvent,
		func(requestContext context.Context, decisionRequest Request, downstream plugin.Next[Request, Outcome]) (decisionOutcome Outcome, invokeErr error) {
			defer func() {
				if panicValue := recover(); panicValue != nil {
					invokeErr = fmt.Errorf("approval: request handler panicked: %v", panicValue)
				}
			}()
			return callback(requestContext, decisionRequest, func(chainContext context.Context) (Outcome, error) {
				return downstream(chainContext, decisionRequest)
			})
		})
}
