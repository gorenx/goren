package middlewareactor

import (
	"errors"

	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

// completeCancel tells the mailbox to reply to CancelCall after collecting
// handler acknowledgements. It does not complete the original Resolve/Invoke.
// envelope identifies the original exchange even if its CallID is later reused.
type completeCancel struct {
	envelope *exchange
	reply    *goakt.GrainReply
	err      error
}

func (owner *Grain) notifyCancellation(gc *goakt.GrainContext, call execution, cause error, reply *goakt.GrainReply) {
	envelope := call.transport()
	count := len(envelope.active)
	if count == 0 {
		if reply != nil {
			reply.Response(Completion{})
		}
		return
	}
	// This bounded channel collects one acknowledgement per current recipient.
	// It does not queue business requests or decide when the Invoke calls finish.
	completed := make(chan error, count)
	stopped := owner.stopped
	if err := gc.PipeToSelf(func() (any, error) {
		var failure error
		for range count {
			select {
			case result := <-completed:
				failure = errors.Join(failure, result)
			case <-stopped:
				if reply != nil {
					reply.Response(Completion{
						Err: errors.Join(failure, errors.New("agent middleware: stopped during cancellation delivery")),
					})
				}
				return nil, nil
			}
		}
		return completeCancel{
			envelope: envelope,
			reply:    reply,
			err:      failure,
		}, nil
	}); err != nil {
		if reply == nil {
			envelope.failed = errors.Join(envelope.failed, err)
		}
		if reply != nil {
			reply.Response(Completion{
				Err: err,
			})
		}
		return
	}
	for identity := range envelope.active {
		gc.RequestGrain(envelope.routes[identity], CancelInvocation{
			CallID:  envelope.identity,
			Handler: identity,
			Cause:   cause,
		}, goakt.WithReentrancyMode(reentrancy.AllowAll)).Then(func(payload any, failure error) {
			if failure == nil {
				result, ok := payload.(Completion)
				if !ok {
					failure = errors.New("agent middleware: invalid cancellation acknowledgement")
				} else {
					failure = result.Err
				}
			}
			completed <- failure
		})
	}
}
