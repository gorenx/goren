package workspaceadapter

import (
	"context"
	"errors"

	"github.com/gorenx/goren/workspace"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Notices consumes committed changes by publishing to a bounded in-memory feed.
// Implementations must not perform blocking I/O inside the subscriber's mailbox.
type Notices interface {
	ObserveEvent(context.Context, workspace.Notice) error
}

// Subscriber forwards Workspace commits to the API without owning domain state.
type Subscriber struct {
	consumer Notices
	ready    chan struct{}
}

// SpawnSubscriber waits for subscription acknowledgement before returning success.
// If cancellation occurs after spawn, the returned PID must still be shut down.
func SpawnSubscriber(ctx context.Context, system goakt.ActorSystem, name string, consumer Notices) (*goakt.PID, error) {
	if ctx == nil ||
		system == nil ||
		consumer == nil {
		return nil, errors.New("workspace API: context, actor system and notice consumer are required")
	}
	err := ctx.Err()
	if err != nil {
		return nil, err
	}
	if system.TopicActor() == nil {
		return nil, errors.New("workspace API: PubSub must be enabled")
	}
	listener := &Subscriber{
		consumer: consumer,
		ready:    make(chan struct{}),
	}
	endpoint, err := system.Spawn(ctx, name, listener, goakt.WithLongLived())
	if err != nil {
		return nil, err
	}
	select {
	case <-listener.ready:
		return endpoint, nil
	case <-ctx.Done():
		return endpoint, context.Cause(ctx)
	}
}

// PreStart leaves registration to PostStart, when the actor has a stable PID.
func (*Subscriber) PreStart(*goakt.Context) error { return nil }

// PostStop leaves consumer lifecycle to composition. TopicActor removes the
// subscription when it observes this subscriber's framework termination.
func (*Subscriber) PostStop(*goakt.Context) error { return nil }

// Receive serializes notification forwarding; Gateway owns frame projection.
func (listener *Subscriber) Receive(rc *goakt.ReceiveContext) {
	switch input := rc.Message().(type) {
	case *goakt.PostStart:
		rc.Tell(rc.ActorSystem().TopicActor(), goakt.NewSubscribe(workspace.Topic))
	case *goakt.SubscribeAck:
		if input.Topic() == workspace.Topic {
			select {
			case <-listener.ready:
			default:
				close(listener.ready)
			}
		}
	case workspace.Notice:
		err := listener.consumer.ObserveEvent(rc.Context(), input)
		if err != nil {
			rc.Logger().Error(err)
		}
	default:
		rc.Unhandled()
	}
}
