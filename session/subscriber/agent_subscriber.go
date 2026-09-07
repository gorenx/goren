// Package subscriber connects Agent creation events to Session initialization.
package subscriber

import (
	"context"
	"errors"
	sessionregistry "github.com/gorenx/goren/session/registry"

	"github.com/google/uuid"
	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// InitializedTopic carries results of event-triggered Session initialization.
const InitializedTopic = "session/initialized"

// InitializedNotice reports a component result, not overall Agent readiness.
// Consumers must match both identities before applying it to an Agent.
type InitializedNotice struct {
	SessionID session.SessionID
	// CreationID correlates this result with the original Agent creation.
	CreationID string
	Header     session.Header
	Barrier    session.WriteBarrier
	Err        error
}

// AgentsSubscribed confirms that the Session initializer can receive Creating events.
type AgentsSubscribed struct{}

// AgentSubscriber translates creation events into Registry requests without owning state.
// Spawn it with request reentrancy enabled; Registry owns all initialization work.
type AgentSubscriber struct {
	registry *goakt.GrainIdentity
	startup  *goakt.PID
}

// NewAgentSubscriber binds the Registry and the observer of subscription readiness.
func NewAgentSubscriber(registry *goakt.GrainIdentity, startup *goakt.PID) (*AgentSubscriber, error) {
	if registry == nil || startup == nil {
		return nil, errors.New("session: subscriber Registry and startup observer are required")
	}
	return &AgentSubscriber{
		registry: registry,
		startup:  startup,
	}, nil
}

// PreStart leaves subscription to the initial mailbox turn.
func (*AgentSubscriber) PreStart(*goakt.Context) error { return nil }

// PostStop relies on TopicActor monitoring to remove a stopped subscriber.
// In-flight Registry initialization remains owned by the Registry.
func (*AgentSubscriber) PostStop(*goakt.Context) error { return nil }

// Receive forwards identity-only triggers and publishes their asynchronous results.
func (owner *AgentSubscriber) Receive(rc *goakt.ReceiveContext) {
	switch incoming := rc.Message().(type) {
	case *goakt.PostStart:
		rc.Tell(rc.ActorSystem().TopicActor(), goakt.NewSubscribe(agent.CreatingTopic))
	case *goakt.SubscribeAck:
		if incoming.Topic() == agent.CreatingTopic {
			rc.Tell(owner.startup, AgentsSubscribed{})
		}
	case agent.CreatingNotice:
		owner.startSession(rc, incoming)
	default:
		rc.Unhandled()
	}
}

func (owner *AgentSubscriber) startSession(rc *goakt.ReceiveContext, input agent.CreatingNotice) {
	topic := rc.ActorSystem().TopicActor()
	ctx := context.WithoutCancel(rc.Context())
	logger := rc.Logger()
	rc.RequestGrain(
		owner.registry,
		sessionregistry.StartSession{
			SessionID:  input.AgentID,
			CreationID: input.CreationID,
		},
		goakt.WithRequestTimeout(0),
	).Then(
		func(payload any, failure error) {
			result := InitializedNotice{
				SessionID:  input.AgentID,
				CreationID: input.CreationID,
				Err:        failure,
			}
			if failure == nil {
				initialized, ok := payload.(sessionactor.Initialized)
				if !ok {
					result.Err = errors.New("session: invalid Registry initialization result")
				} else {
					result.Header = initialized.Header
					result.Barrier = initialized.Barrier
					result.Err = initialized.Err
				}
			}
			if err := goakt.Tell(ctx, topic, goakt.NewPublish(uuid.NewString(), InitializedTopic, result)); err != nil {
				logger.Error("session: failed to publish initialization result")
			}
		},
	)
}
