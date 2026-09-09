package toolactor

import (
	"errors"

	"github.com/google/uuid"
	toolregistry "github.com/gorenx/goren/tools/registry"
	goakt "github.com/tochemey/goakt/v4/actor"
)

func (owner *RegistryGrain) receiveConfiguration(gc *goakt.GrainContext) bool {
	switch incoming := gc.Message().(type) {
	case InitializeDefaults:
		gc.Response(Completion{
			Err: owner.registry.InitializeDefaults(incoming.Tools),
		})
	case RegisterConfiguration:
		change, err := owner.registry.Configure(
			incoming.ID,
			incoming.AgentID,
			incoming.Overrides,
		)
		if err != nil {
			gc.Response(Configuration{
				Err: err,
			})
			return true
		}
		captured, found := owner.registry.ReadConfiguration(change.ID)
		gc.Response(Configuration{
			Value: captured,
			Found: found,
			Err:   owner.publishEvent(gc, toolregistry.ConfigurationTopic, change),
		})
	case ReadConfiguration:
		captured, found := owner.registry.ReadConfiguration(incoming.ID)
		gc.Response(Configuration{
			Value: captured,
			Found: found,
		})
	default:
		return false
	}
	return true
}

// publishEvent is the framework erasure boundary for the two typed registry
// events. A delivery error never rolls back the preceding business operation.
func (*RegistryGrain) publishEvent(gc *goakt.GrainContext, topic string, event any) error {
	target := gc.ActorSystem().TopicActor()
	if target == nil {
		return errors.New("tools: registry change committed but PubSub is unavailable")
	}
	return goakt.Tell(gc.Context(), target, goakt.NewPublish(uuid.NewString(), topic, event))
}
