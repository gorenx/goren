package runtimeactor

import (
	"context"
	"errors"

	"github.com/gorenx/goren/tools/execution"

	"github.com/gorenx/goren/agentmessage"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// invocation retains one call and its current transport reply. Business stages
// and actual results belong to Call; cancellation does not release this record.
type invocation struct {
	business *execution.Call
	cancel   context.CancelFunc
	reply    *goakt.GrainReply
}

type callCompleted struct {
	invocation *invocation
	business   *execution.Call
	result     CallResult
}

func (owner *Runtime) receiveCalls(gc *goakt.GrainContext) bool {
	switch input := gc.Message().(type) {
	case PrepareCall:
		owner.prepareCall(gc, input)
	case DispatchCall:
		owner.requestCall(gc, input.CreationID, input.CallID, true, func(current *execution.Call) CallResult {
			return CallResult{
				Err: current.Dispatch(),
			}
		})
	case FinalizeCall:
		owner.requestCall(gc, input.CreationID, input.CallID, false, func(current *execution.Call) CallResult {
			err := current.Finalize()
			if err != nil {
				return CallResult{
					Err: err,
				}
			}
			result, err := current.Result()
			return CallResult{
				Result: result,
				Err:    err,
			}
		})
	case CancelCall:
		current, err := owner.findCall(input.CreationID, input.CallID)
		if err == nil {
			current.cancel()
		}
		gc.Response(Completion{
			Err: err,
		})
	case ReleaseCall:
		current, err := owner.findCall(input.CreationID, input.CallID)
		if err == nil && current.reply != nil {
			err = errors.New("tools: call operation is still running")
		}
		if err == nil && current.business != nil {
			_, err = current.business.Result()
		}
		if err == nil {
			current.cancel()
			delete(owner.calls, input.CallID)
		}
		gc.Response(Completion{
			Err: err,
		})
	case callCompleted:
		gc.NoErr()
		current := input.invocation
		if owner.policyRequest == current {
			owner.policyRequest = nil
		}
		if current.reply == nil {
			return true
		}
		if input.business != nil {
			current.business = input.business
		}
		current.reply.Response(input.result)
		current.reply = nil
	default:
		return false
	}
	return true
}

func (owner *Runtime) findCall(creationID string, identifier agentmessage.CallID) (*invocation, error) {
	if owner.creationID == "" || owner.creationID != creationID {
		return nil, errors.New("tools: call does not own this runtime")
	}
	current := owner.calls[identifier]
	if current == nil {
		return nil, errors.New("tools: call is not registered")
	}
	return current, nil
}

func (owner *Runtime) prepareCall(gc *goakt.GrainContext, input PrepareCall) {
	if owner.creationID == "" ||
		owner.creationID != input.CreationID ||
		owner.toolRuntime == nil ||
		owner.active != nil ||
		owner.policyRequest != nil ||
		input.Input.SessionID != owner.agentID ||
		input.Input.CallID == "" ||
		owner.calls[input.Input.CallID] != nil {
		gc.Response(CallResult{
			Err: errors.New("tools: call preparation requires an available runtime and unique call identity"),
		})
		return
	}
	snapshot, err := owner.toolRuntime.Snapshot()
	if err != nil {
		gc.Response(CallResult{
			Err: err,
		})
		return
	}
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(CallResult{
			Err: errors.New("tools: asynchronous call request required"),
		})
		return
	}
	ctx := input.Context
	if ctx == nil {
		ctx = context.WithoutCancel(gc.Context())
	}
	ctx, cancel := context.WithCancel(ctx)
	current := &invocation{
		cancel: cancel,
		reply:  reply,
	}
	if owner.calls == nil {
		owner.calls = make(map[agentmessage.CallID]*invocation)
	}
	owner.calls[input.Input.CallID] = current
	owner.policyRequest = current
	input.Input.Arguments = append([]byte(nil), input.Input.Arguments...)
	instances := owner.instances
	policies := snapshot.Policies[input.Input.Name]
	err = gc.PipeToSelf(func() (payload any, failure error) {
		defer recoverCall(current, &payload)
		coordinator, failure := instances.Scheduler(ctx, snapshot, policies)
		if failure != nil {
			return callCompleted{
				invocation: current,
				result: CallResult{
					Err: failure,
				},
			}, nil
		}
		business, failure := coordinator.PrepareCall(ctx, input.Input)
		result := CallResult{
			Err: failure,
		}
		if failure == nil {
			result.Stage = business.Preparation()
		}
		return callCompleted{
			invocation: current,
			business:   business,
			result:     result,
		}, nil
	})
	if err != nil {
		if owner.policyRequest == current {
			owner.policyRequest = nil
		}
		current.reply.Response(CallResult{
			Err: err,
		})
		current.reply = nil
	}
}

func (owner *Runtime) requestCall(gc *goakt.GrainContext, creationID string, identifier agentmessage.CallID, overlap bool, run func(*execution.Call) CallResult) {
	current, err := owner.findCall(creationID, identifier)
	if err == nil && !overlap && owner.policyRequest != nil {
		err = errors.New("tools: another policy request is running")
	}
	if err == nil && (current.reply != nil || current.business == nil) {
		err = errors.New("tools: call has no available business operation")
	}
	if err != nil {
		gc.Response(CallResult{
			Err: err,
		})
		return
	}
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(CallResult{
			Err: errors.New("tools: asynchronous call request required"),
		})
		return
	}
	current.reply = reply
	if !overlap {
		owner.policyRequest = current
	}
	business := current.business
	err = gc.PipeToSelf(func() (payload any, failure error) {
		defer recoverCall(current, &payload)
		return callCompleted{
			invocation: current,
			result:     run(business),
		}, nil
	})
	if err != nil {
		if owner.policyRequest == current {
			owner.policyRequest = nil
		}
		current.reply.Response(CallResult{
			Err: err,
		})
		current.reply = nil
	}
}

// recoverCall always returns a correlated completion to the mailbox. It does not
// certify body exit or synthesize a business result after an unexpected panic.
func recoverCall(current *invocation, payload *any) {
	if recover() != nil {
		*payload = callCompleted{
			invocation: current,
			result: CallResult{
				Err: errors.New("tools: call operation panicked"),
			},
		}
	}
}
