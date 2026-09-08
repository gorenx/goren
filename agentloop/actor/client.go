package loopactor

import (
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

// Client addresses one Agent creation. It submits asynchronous runtime requests;
// it does not implement agent.Agent or retain a pooled receive context.
// The calling Grain must enable framework reentrancy before using the client.
type Client struct {
	target     *goakt.GrainIdentity
	creationID string
}

// NewClient fixes the creation credential so an old client cannot command a replacement.
func NewClient(target *goakt.GrainIdentity, creationID string) (*Client, error) {
	if target == nil || creationID == "" {
		return nil, errors.New("agentloop: client requires an Agent route and creation identity")
	}
	if err := target.Validate(); err != nil {
		return nil, err
	}
	return &Client{
		target:     target,
		creationID: creationID,
	}, nil
}

// Followup submits input; its continuation acknowledges the Inbox commit only.
func (sender *Client) Followup(scope *goakt.GrainContext, input agentmessage.UserMessage) ChangeRequest {
	return ChangeRequest{
		call: scope.RequestGrain(sender.target, Followup{
			CreationID: sender.creationID,
			Message:    input,
		}, goakt.WithRequestTimeout(0), goakt.WithReentrancyMode(reentrancy.AllowAll)),
	}
}

// Steer submits input for the business owner's next admissible execution boundary.
func (sender *Client) Steer(scope *goakt.GrainContext, input agentmessage.UserMessage) ChangeRequest {
	return ChangeRequest{
		call: scope.RequestGrain(sender.target, Steer{
			CreationID: sender.creationID,
			Message:    input,
		}, goakt.WithRequestTimeout(0), goakt.WithReentrancyMode(reentrancy.AllowAll)),
	}
}

// Inject submits input without requesting execution.
func (sender *Client) Inject(scope *goakt.GrainContext, input agentmessage.UserMessage) ChangeRequest {
	return ChangeRequest{
		call: scope.RequestGrain(sender.target, Inject{
			CreationID: sender.creationID,
			Message:    input,
		}, goakt.WithRequestTimeout(0), goakt.WithReentrancyMode(reentrancy.AllowAll)),
	}
}

// Cancel requests cancellation delivery and optional Inbox clearing.
// Completion does not imply that execution or resource cleanup has finished.
func (sender *Client) Cancel(scope *goakt.GrainContext, intent agent.CancelCause, options agent.CancelOptions) ChangeRequest {
	return ChangeRequest{
		call: scope.RequestGrain(sender.target, Cancel{
			CreationID: sender.creationID,
			Cause:      intent,
			Options:    options,
		}, goakt.WithRequestTimeout(0), goakt.WithReentrancyMode(reentrancy.AllowAll)),
	}
}

// WhenIdle requests confirmation that execution and cleanup have finished.
// It leaves the caller's mailbox available for cancellation and other work.
func (sender *Client) WhenIdle(scope *goakt.GrainContext) IdleRequest {
	return IdleRequest{
		call: scope.RequestGrain(sender.target, WhenIdle{
			CreationID: sender.creationID,
		}, goakt.WithRequestTimeout(0), goakt.WithReentrancyMode(reentrancy.AllowAll)),
	}
}

// ChangeRequest is a typed continuation over the framework's request handle.
type ChangeRequest struct {
	call goakt.RequestCall
}

// Then runs on the requesting Grain's turn. No business state is accessed off-mailbox.
func (request ChangeRequest) Then(receive func(Changed)) {
	request.call.Then(func(payload any, failure error) {
		result, ok := payload.(Changed)
		if failure != nil {
			result.Err = failure
		} else if !ok {
			result.Err = errors.New("agentloop: invalid input response")
		}
		receive(result)
	})
}

// IdleRequest is the runtime completion request, separate from input admission.
type IdleRequest struct {
	call goakt.RequestCall
}

// Then delivers idle confirmation or failure on the requesting Grain's turn.
func (request IdleRequest) Then(receive func(Completion)) {
	request.call.Then(func(payload any, failure error) {
		result, ok := payload.(Completion)
		if failure != nil {
			result.Err = failure
		} else if !ok {
			result.Err = errors.New("agentloop: invalid idle response")
		}
		receive(result)
	})
}
