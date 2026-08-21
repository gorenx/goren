package plugin

import (
	"context"
	"errors"
)

type runtimeCallbackKey struct{}

func (runtimeEngine *Runtime) beginOperation(operationContext context.Context) error {
	if operationContext == nil {
		return errors.New("plugin: Runtime operation Context is nil")
	}
	if activeRuntime, _ := operationContext.Value(runtimeCallbackKey{}).(*Runtime); activeRuntime == runtimeEngine {
		return ErrTopologyMutation
	}
	select {
	case <-operationContext.Done():
		return context.Cause(operationContext)
	case <-runtimeEngine.operations:
		return nil
	}
}

func (runtimeEngine *Runtime) endOperation() {
	runtimeEngine.operations <- struct{}{}
}

func (runtimeEngine *Runtime) callbackContext(requestContext context.Context) context.Context {
	if requestContext == nil {
		requestContext = context.Background()
	}
	return context.WithValue(requestContext, runtimeCallbackKey{}, runtimeEngine)
}
