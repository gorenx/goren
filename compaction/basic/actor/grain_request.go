package compactionactor

import (
	"context"
	goakt "github.com/tochemey/goakt/v4/actor"
	"time"
)

// exchange retains one dependency operation across transport retries.
// message keeps the same Session proposal and idempotency identity.
type exchange struct {
	invocation *invocation
	target     *goakt.GrainIdentity
	message    any
	purpose    replyPurpose
}

type replyReceived struct {
	request *exchange
	payload any
	err     error
}

type retryRequest struct {
	request *exchange
}

func (owner *Grain) request(gc *goakt.GrainContext, current *invocation, target *goakt.GrainIdentity, message any, purpose replyPurpose) {
	owner.sendRequest(gc, &exchange{
		invocation: current,
		target:     target,
		message:    message,
		purpose:    purpose,
	})
}

// sendRequest waits outside the mailbox without reading mutable actor state.
func (owner *Grain) sendRequest(gc *goakt.GrainContext, pending *exchange) {
	system := gc.ActorSystem()
	ctx := context.WithoutCancel(gc.Context())
	timeout := owner.timeout
	if err := gc.PipeToSelf(func() (any, error) {
		payload, failure := system.AskGrain(ctx, pending.target, pending.message, timeout)
		return replyReceived{
			request: pending,
			payload: payload,
			err:     failure,
		}, nil
	}); err != nil {
		pending.invocation.reply.Err(err)
	}
}

// receiveRequest advances a known result or schedules the same operation again.
// A reply for a retired invocation cannot change the current business state.
func (owner *Grain) receiveRequest(gc *goakt.GrainContext) bool {
	switch input := gc.Message().(type) {
	case replyReceived:
		gc.NoErr()
		pending := input.request
		if owner.active != pending.invocation {
			return true
		}
		if input.err == nil && owner.acceptReply(gc, pending, input.payload) {
			return true
		}
		if _, err := gc.ScheduleOnce(retryRequest{
			request: pending,
		}, 100*time.Millisecond); err != nil {
			pending.invocation.reply.Err(err)
		}
	case retryRequest:
		gc.NoErr()
		if owner.active == input.request.invocation {
			owner.sendRequest(gc, input.request)
		}
	default:
		return false
	}
	return true
}
