package titleactor

import (
	"context"
	"errors"

	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	"github.com/gorenx/goren/session/title"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Subscriber forwards notifications for one creation, without owning title state.
// Composition enables AllowAll requests and waits for Subscribed before admission.
type Subscriber struct {
	target     *goakt.GrainIdentity
	creationID string
	startup    *goakt.PID
}

// Subscribed confirms both Session topics are attached.
type Subscribed struct{}

type stopSubscriber struct{}

// NewSubscriber binds an initialized Title Grain to its exact creation.
func NewSubscriber(target *goakt.GrainIdentity, creationID string, startup *goakt.PID) (*Subscriber, error) {
	if target == nil || creationID == "" || startup == nil {
		return nil, errors.New("title: subscription target, creation and startup observer are required")
	}
	return &Subscriber{
		target:     target,
		creationID: creationID,
		startup:    startup,
	}, nil
}

func (*Subscriber) PreStart(*goakt.Context) error { return nil }
func (*Subscriber) PostStop(*goakt.Context) error { return nil }

// Receive filters unrelated creations before issuing asynchronous requests.
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
		if input.CreationID != listener.creationID || RuntimeName(string(input.SessionID)) != listener.target.Name() {
			return
		}
		logger := rc.ActorSystem().Logger()
		rc.RequestGrain(listener.target, input.Clone(), goakt.WithRequestTimeout(0)).Then(func(payload any, failure error) {
			if failure == nil {
				result, ok := payload.(Updated)
				if !ok {
					failure = errors.New("title: invalid event acknowledgement")
				} else {
					failure = result.Err
				}
			}
			if failure != nil && !errors.Is(failure, title.ErrSuperseded) && !errors.Is(failure, context.Canceled) {
				logger.Errorf("title: automatic processing failed: %v", failure)
			}
		})
	case sessionactor.ClosedNotice:
		if input.CreationID != listener.creationID || RuntimeName(string(input.SessionID)) != listener.target.Name() {
			return
		}
		ctx, self, logger := context.WithoutCancel(rc.Context()), rc.Self(), rc.ActorSystem().Logger()
		rc.RequestGrain(listener.target, input, goakt.WithRequestTimeout(0)).Then(func(payload any, failure error) {
			if failure == nil {
				result, ok := payload.(Completion)
				if !ok {
					failure = errors.New("title: invalid close acknowledgement")
				} else {
					failure = result.Err
				}
			}
			if failure != nil {
				logger.Errorf("title: close notification failed: %v", failure)
				return
			}
			if err := goakt.Tell(ctx, self, stopSubscriber{}); err != nil {
				logger.Errorf("title: stop subscriber: %v", err)
			}
		})
	case stopSubscriber:
		rc.Shutdown()
	default:
		rc.Unhandled()
	}
}
