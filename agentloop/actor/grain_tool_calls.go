package loopactor

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agentloop"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/tools"
	runtimeactor "github.com/gorenx/goren/tools/runtime/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

// toolPort adapts individual requests without selecting stages or batch order.
type toolPort struct{ scope serviceScope }

func (endpoint toolPort) Prepare(input tools.ToolExecutionInput, receiver agentloop.ToolReceiver) error {
	owner := endpoint.scope.owner
	if owner.calls == nil {
		owner.calls = make(map[string]struct{})
	}
	owner.calls[string(input.CallID)] = struct{}{}
	return endpoint.request(runtimeactor.PrepareCall{
		CreationID: owner.creationID,
		Input:      input,
		Context:    context.WithoutCancel(endpoint.scope.gc.Context()),
	}, func(result runtimeactor.CallResult) func(agentloop.Services) {
		return func(services agentloop.Services) {
			receiver.ToolPrepared(services, result.Stage, result.Err)
		}
	})
}

func (endpoint toolPort) Dispatch(identifier agentmessage.CallID, receiver agentloop.ToolReceiver) error {
	return endpoint.request(runtimeactor.DispatchCall{
		CreationID: endpoint.scope.owner.creationID,
		CallID:     identifier,
	}, func(result runtimeactor.CallResult) func(agentloop.Services) {
		return func(services agentloop.Services) { receiver.ToolDispatched(services, result.Err) }
	})
}

func (endpoint toolPort) Finalize(identifier agentmessage.CallID, receiver agentloop.ToolReceiver) error {
	return endpoint.request(runtimeactor.FinalizeCall{
		CreationID: endpoint.scope.owner.creationID,
		CallID:     identifier,
	}, func(result runtimeactor.CallResult) func(agentloop.Services) {
		return func(services agentloop.Services) { receiver.ToolFinalized(services, result.Result, result.Err) }
	})
}

func (endpoint toolPort) Release(identifier agentmessage.CallID, receiver agentloop.ToolReceiver) error {
	return endpoint.scope.request(endpoint.scope.owner.tools, runtimeactor.ReleaseCall{
		CreationID: endpoint.scope.owner.creationID,
		CallID:     identifier,
	}, nil, reentrancy.AllowAll, func(payload any, failure error) func(agentloop.Services) {
		result, valid := payload.(runtimeactor.Completion)
		if failure == nil {
			if !valid {
				failure = errors.New("agentloop: invalid Tool release response")
			} else {
				failure = result.Err
			}
		}
		return func(services agentloop.Services) {
			if failure == nil {
				port := services.Tools.(toolPort)
				delete(port.scope.owner.calls, string(identifier))
			}
			receiver.ToolReleased(services, failure)
		}
	})
}

func (endpoint toolPort) request(message any, deliver func(runtimeactor.CallResult) func(agentloop.Services)) error {
	return endpoint.scope.request(endpoint.scope.owner.tools, message, nil, reentrancy.AllowAll,
		func(payload any, failure error) func(agentloop.Services) {
			result, valid := payload.(runtimeactor.CallResult)
			if failure != nil {
				result.Err = failure
			} else if !valid {
				result.Err = errors.New("agentloop: invalid Tool response")
			}
			return deliver(result)
		})
}
