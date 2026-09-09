package eventadapter

import (
	"context"
	"errors"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Events consumes Session facts and Agent execution notices. Committed calls
// are serialized and must stop on cancellation. StatusChanged publishes to a
// bounded feed without I/O and may run while Committed awaits a snapshot.
type Events interface {
	StatusChanged(agent.StatusNotice) error
	Committed(context.Context, session.SessionID, string, session.Event) error
}

// Subscriber serializes committed-event delivery without owning Session state.
type Subscriber struct {
	consumer Events
	ready    chan struct{}
	// subscriptions maps each pending topic name to its acknowledgement responsibility.
	subscriptions map[string]struct{}
	context       context.Context
	cancel        context.CancelFunc
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
func SpawnSubscriber(ctx context.Context, system goakt.ActorSystem, name string, consumer Events) (*goakt.PID, error) {
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
		subscriptions: map[string]struct{}{
			session.AppendedEventName: {},
			agent.StatusTopic:         {},
		},
		context: lifetime,
		cancel:  cancel,
	}
	endpoint, err := system.Spawn(ctx, name, listener, goakt.WithStashing(), goakt.WithLongLived())
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
		for topic := range listener.subscriptions {
			rc.Tell(rc.ActorSystem().TopicActor(), goakt.NewSubscribe(topic))
		}
	case *goakt.SubscribeAck:
		delete(listener.subscriptions, input.Topic())
		if len(listener.subscriptions) == 0 {
			select {
			case <-listener.ready:
			default:
				close(listener.ready)
			}
		}
	case agent.StatusNotice:
		err := listener.consumer.StatusChanged(input)
		if err != nil {
			rc.Logger().Error(err)
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
