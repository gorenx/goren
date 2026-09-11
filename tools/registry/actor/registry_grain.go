package toolactor

import (
	"context"
	"errors"

	"github.com/google/uuid"
	toolregistry "github.com/gorenx/goren/tools/registry"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// RegistryGrain serializes schema configuration and adapts callable addresses.
// It does not construct tool implementations or own Runtime update transactions.
type RegistryGrain struct {
	registry toolregistry.Registry
	// actors maps (AgentID, canonical tool name) to an initialized ToolActor's
	// address and CreationID, e.g. ("session-1", "read_file"). CreationID prevents
	// a delayed withdrawal from removing the next lifetime at the same address.
	actors map[toolName]address
}

// OnActivate acquires no resources; Registry has a usable zero value.
func (*RegistryGrain) OnActivate(context.Context, *goakt.GrainProps) error { return nil }

// OnDeactivate releases no tool resources; their lifetime owner closes them first.
func (*RegistryGrain) OnDeactivate(context.Context, *goakt.GrainProps) error { return nil }

// OnReceive delegates configuration decisions and sends scope-only notifications.
func (owner *RegistryGrain) OnReceive(gc *goakt.GrainContext) {
	if owner.receiveTools(gc) {
		return
	}
	var change ConfigurationChanged
	var err error
	switch input := gc.Message().(type) {
	case RegisterSource:
		err = owner.registry.RegisterSource(input.Name, input.Catalog)
	case RemoveSource:
		owner.registry.RemoveSource(input.Name)
	case ConfigureSession:
		change.SessionID = input.SessionID
		err = owner.registry.ConfigureSession(input.SessionID, input.Catalog)
	case RemoveSession:
		change.SessionID = input.SessionID
		owner.registry.RemoveSession(input.SessionID)
	case ReadConfiguration:
		gc.Response(Configuration{
			Value: owner.registry.Read(input.SessionID),
		})
		return
	default:
		gc.Unhandled()
		return
	}
	if err == nil {
		topic := gc.ActorSystem().TopicActor()
		if topic == nil {
			err = errors.New("tools: configuration saved but PubSub is unavailable")
		} else {
			err = goakt.Tell(gc.Context(), topic, goakt.NewPublish(uuid.NewString(), ConfigurationTopic, change))
		}
	}
	gc.Response(Completion{
		Err: err,
	})
}
