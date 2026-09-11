package execution

import (
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/approval"
	"github.com/gorenx/goren/tools"
)

// requestApproval uses the same named body boundary as normal tool execution.
// Approval owns its policy, interaction and audit; this scheduler only consumes
// its terminal outcome and continues the already-prepared original invocation.
func (coordinator *Scheduler) requestApproval(prepared preparation) (preparation, error) {
	original := prepared.Execution
	ctx := original.state.requestContext
	if ctx.Err() != nil {
		return coordinator.applyApproval(original, approval.OutcomeCancelled)
	}
	entry := coordinator.definitions[approval.ServiceName]
	if entry == nil || original.Name == approval.ServiceName {
		return coordinator.applyApproval(original, approval.OutcomeUnavailable)
	}
	request := prepared.Approval
	if request == nil {
		return finalPreparation(original, errorResult(errors.New("tools: approval request is missing"))), nil
	}
	arguments, err := json.Marshal(request)
	if err != nil {
		return finalPreparation(original, errorResult(err)), nil
	}
	err = entry.ValidateArguments(arguments)
	if err != nil {
		return finalPreparation(original, errorResult(err)), nil
	}
	reply, err := invokeBody(coordinator.dispatcher.bodies, ctx, tools.Invocation{
		CallID:     agentmessage.CallID(uuid.NewString()),
		RootCallID: original.RootCallID,
		Name:       approval.ServiceName,
		SessionID:  original.SessionID,
		Arguments:  arguments,
	})
	if err != nil {
		return prepared, &DeliveryError{
			Cause: err,
		}
	}
	if reply.Err != nil {
		return finalPreparation(original, errorResult(reply.Err)), nil
	}
	err = entry.ValidateValue(reply.Value)
	if err != nil {
		return finalPreparation(original, errorResult(err)), nil
	}
	var answer approval.Outcome
	err = json.Unmarshal(reply.Value, &answer)
	if err != nil {
		return finalPreparation(original, errorResult(err)), nil
	}
	return coordinator.applyApproval(original, answer)
}
