package runtimeactor

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/gorenx/goren/tools"
	catalogactor "github.com/gorenx/goren/tools/registry/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

func (owner *Runtime) receiveConfiguration(gc *goakt.GrainContext) bool {
	switch input := gc.Message().(type) {
	case configurationNotice:
		gc.Response(Completion{})
		if input.creationID == owner.creationID {
			owner.changed = true
		}
	case RefreshConfiguration:
		if input.ID == "" ||
			input.CreationID != owner.creationID ||
			owner.toolRuntime == nil ||
			owner.active != nil ||
			len(owner.calls) != 0 {
			gc.Response(Definitions{Err: errors.New("tools: configuration requires an idle runtime and current creation")})
			return true
		}
		if owner.connections.changed() {
			owner.changed = true
		}
		if !owner.changed {
			definitions, err := owner.toolRuntime.Definitions()
			gc.Response(Definitions{
				Value: definitions,
				Err:   err,
			})
			return true
		}
		reply := gc.DeferResponse()
		if reply == nil {
			gc.Response(Definitions{Err: errors.New("tools: asynchronous configuration request required")})
			return true
		}
		current := &operation{
			reply:     reply,
			refreshID: input.ID,
		}
		owner.active = current
		owner.changed = false
		business := owner.toolRuntime
		directory := owner.catalog
		instances := owner.instances
		peers := owner.connections
		agentID := owner.agentID
		owner.schedule(gc, current, func(ctx context.Context) error {
			configuration, err := directory.configuration(ctx, agentID)
			if err != nil {
				return err
			}
			definitions := append(configuration.Global.MCP, configuration.Custom.MCP...)
			if err := peers.reconcile(ctx, definitions); err != nil {
				return err
			}
			if err := peers.refresh(ctx); err != nil {
				return err
			}
			if _, err := business.Apply(ctx, directory, instances); err != nil {
				return err
			}
			applied, err := business.Snapshot()
			if err != nil {
				return err
			}
			return peers.retire(ctx, applied.ProviderIDs)
		})
	case CancelConfiguration:
		if input.CreationID == owner.creationID &&
			owner.active != nil &&
			owner.active.refreshID == input.ID &&
			owner.active.cancel != nil {
			owner.active.cancel()
		}
		gc.Response(Completion{})
	default:
		return false
	}
	return true
}

func (owner *Runtime) finishRefresh(gc *goakt.GrainContext, current *operation, failure error) {
	if owner.active != current {
		return
	}
	current.cancel()
	owner.active = nil
	if failure != nil {
		owner.changed = true
		current.reply.Response(Definitions{Err: failure})
		return
	}
	definitions, err := owner.toolRuntime.Definitions()
	if err == nil {
		topic := gc.ActorSystem().TopicActor()
		if topic == nil {
			err = errors.New("tools: applied configuration could not be published")
		} else {
			err = goakt.Tell(gc.Context(), topic, goakt.NewPublish(uuid.NewString(), tools.DefinitionsTopic, definitions))
		}
	}
	if err != nil {
		owner.changed = true
	}
	current.reply.Response(Definitions{
		Value: definitions,
		Err:   err,
	})
}

// Watcher forwards scope-relevant invalidation only; it never merges or applies schemas.
// Agent lifecycle owns this Actor and stops it before releasing the Runtime.
type Watcher struct {
	AgentID    string
	CreationID string
	Target     *goakt.GrainIdentity
}

type configurationNotice struct{ creationID string }

// PreStart acquires no business resources.
func (*Watcher) PreStart(*goakt.Context) error { return nil }

// PostStop leaves PubSub membership cleanup to the framework's terminated-subscriber tracking.
func (*Watcher) PostStop(*goakt.Context) error { return nil }

// Receive subscribes once and forwards configuration facts to the exact Runtime lifetime.
func (owner *Watcher) Receive(rc *goakt.ReceiveContext) {
	switch input := rc.Message().(type) {
	case *goakt.PostStart:
		rc.Tell(rc.ActorSystem().TopicActor(), goakt.NewSubscribe(catalogactor.ConfigurationTopic))
	case *goakt.SubscribeAck:
		if input.Topic() == catalogactor.ConfigurationTopic {
			owner.notify(rc)
		}
	case catalogactor.ConfigurationChanged:
		if input.SessionID == "" || string(input.SessionID) == owner.AgentID {
			owner.notify(rc)
		}
	}
}

func (owner *Watcher) notify(rc *goakt.ReceiveContext) {
	call := rc.RequestGrain(owner.Target, configurationNotice{creationID: owner.CreationID})
	if call == nil {
		return
	}
	logger := rc.Logger()
	call.Then(func(_ any, failure error) {
		if failure != nil {
			logger.Error(failure)
		}
	})
}
