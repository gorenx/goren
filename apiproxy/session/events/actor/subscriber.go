package eventadapter

import (
	"context"
	"errors"
	"sync"

	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Commits consumes detached Session facts. Calls are serialized and must return
// when their context is canceled so shutdown can wait for their completion.
type Commits interface {
	Committed(context.Context, session.SessionID, string, session.Event) error
}

// Subscriber serializes committed-event delivery without owning Session state.
type Subscriber struct {
	consumer Commits
	ready    chan struct{}
	context  context.Context
	cancel   context.CancelFunc
	// shutdown guards the framework boundary: external PID.Shutdown may invoke
	// PostStop concurrently with Receive. It does not coordinate business work.
	shutdown sync.Mutex
	active   *delivery
}

type delivery struct {
	done chan struct{}
	err  error
}

type delivered struct {
	operation *delivery
}

// SpawnSubscriber waits for PubSub acknowledgement. If waiting is canceled after
// spawn, the returned PID remains the caller's shutdown responsibility.
func SpawnSubscriber(ctx context.Context, system goakt.ActorSystem, name string, consumer Commits) (*goakt.PID, error) {
	if system == nil || consumer == nil {
		return nil, errors.New("session API: actor system and commit consumer are required")
	}
	if system.TopicActor() == nil {
		return nil, errors.New("session API: PubSub must be enabled")
	}
	lifetime, cancel := context.WithCancel(context.Background())
	listener := &Subscriber{
		consumer: consumer,
		ready:    make(chan struct{}),
		context:  lifetime,
		cancel:   cancel,
	}
	endpoint, err := system.Spawn(ctx, name, listener, goakt.WithStashing())
	if err != nil {
		cancel()
		return nil, err
	}
	select {
	case <-listener.ready:
		return endpoint, nil
	case <-ctx.Done():
		return endpoint, context.Cause(ctx)
	}
}

func (*Subscriber) PreStart(*goakt.Context) error { return nil }

// PostStop waits for canceled consumption before releasing its consumer.
func (listener *Subscriber) PostStop(*goakt.Context) error {
	listener.cancel()
	listener.shutdown.Lock()
	defer listener.shutdown.Unlock()
	if listener.active != nil {
		<-listener.active.done
		listener.active = nil
	}
	return nil
}

// Receive keeps later events in the framework stash while consumption runs.
func (listener *Subscriber) Receive(rc *goakt.ReceiveContext) {
	listener.shutdown.Lock()
	defer listener.shutdown.Unlock()
	if listener.context.Err() != nil {
		return
	}
	switch input := rc.Message().(type) {
	case *goakt.PostStart:
		rc.Tell(rc.ActorSystem().TopicActor(), goakt.NewSubscribe(session.AppendedEventName))
	case *goakt.SubscribeAck:
		if input.Topic() == session.AppendedEventName {
			select {
			case <-listener.ready:
			default:
				close(listener.ready)
			}
		}
	case sessionactor.CommitNotice:
		if listener.active != nil {
			rc.Stash()
			return
		}
		fact := input.Clone()
		consumer := listener.consumer
		ctx := listener.context
		current := &delivery{
			done: make(chan struct{}),
		}
		listener.active = current
		if err := rc.Self().PipeTo(ctx, rc.Self(), func() (any, error) {
			defer close(current.done)
			current.err = consumer.Committed(ctx, fact.SessionID, fact.CreationID, fact.Event)
			return delivered{
				operation: current,
			}, nil
		}); err != nil {
			listener.active = nil
			rc.Logger().Error(err)
			rc.UnstashAll()
		}
	case delivered:
		if listener.active != input.operation {
			return
		}
		current := listener.active
		<-current.done
		listener.active = nil
		if current.err != nil {
			rc.Logger().Error(current.err)
		}
		rc.UnstashAll()
	default:
		rc.Unhandled()
	}
}
