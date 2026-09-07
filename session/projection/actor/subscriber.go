package projectionactor

import (
	"errors"
	sessionactor "github.com/gorenx/goren/session/actor"

	"github.com/gorenx/goren/session"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Subscriber forwards Session facts to the projection Grain without owning folds.
// Composition enables request reentrancy and waits for Subscribed before admission.
type Subscriber struct {
	target  *goakt.GrainIdentity
	startup *goakt.PID
}

// NewSubscriber binds the projection owner and the subscription readiness observer.
func NewSubscriber(target *goakt.GrainIdentity, startup *goakt.PID) (*Subscriber, error) {
	if target == nil || startup == nil {
		return nil, errors.New("sessionprojection: owner and startup observer are required")
	}
	return &Subscriber{
		target:  target,
		startup: startup,
	}, nil
}

// PreStart leaves subscription to the first mailbox turn.
func (*Subscriber) PreStart(*goakt.Context) error { return nil }

// PostStop relies on TopicActor monitoring to remove subscriptions.
func (*Subscriber) PostStop(*goakt.Context) error { return nil }

// Receive forwards immutable Session facts using asynchronous framework requests.
func (listener *Subscriber) Receive(rc *goakt.ReceiveContext) {
	switch input := rc.Message().(type) {
	case *goakt.PostStart:
		rc.Tell(rc.ActorSystem().TopicActor(), goakt.NewSubscribe(session.AppendedEventName))
	case *goakt.SubscribeAck:
		switch input.Topic() {
		case session.AppendedEventName:
			rc.Tell(rc.ActorSystem().TopicActor(), goakt.NewSubscribe(sessionactor.ClosedTopic))
		case sessionactor.ClosedTopic:
			rc.Tell(listener.startup, Subscribed{})
		}
	case sessionactor.CommitNotice:
		listener.forward(rc, input.Clone())
	case sessionactor.ClosedNotice:
		listener.forward(rc, input)
	default:
		rc.Unhandled()
	}
}

// forward only erases the two internal notification envelopes at the framework boundary.
func (listener *Subscriber) forward(rc *goakt.ReceiveContext, notice any) {
	logger := rc.ActorSystem().Logger()
	rc.RequestGrain(listener.target, notice).Then(func(payload any, failure error) {
		if failure == nil {
			result, ok := payload.(Completion)
			if !ok {
				failure = errors.New("sessionprojection: invalid notification result")
			} else {
				failure = result.Err
			}
		}
		if failure != nil {
			logger.Errorf("sessionprojection: Session notification processing failed: %v", failure)
		}
	})
}
