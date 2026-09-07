package toolactor

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/gorenx/goren/tools"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// RegistryGrain serializes catalog transactions and publishes committed definitions.
// It must be long-lived: passivation is not a business resource-release protocol.
type RegistryGrain struct {
	registry tools.Registry
}

// OnActivate creates no resources; the business Registry has a usable zero value.
func (*RegistryGrain) OnActivate(context.Context, *goakt.GrainProps) error { return nil }

// OnDeactivate has no external resources to release.
func (*RegistryGrain) OnDeactivate(context.Context, *goakt.GrainProps) error { return nil }

// OnReceive executes short catalog commands without blocking on other Actors.
func (owner *RegistryGrain) OnReceive(gc *goakt.GrainContext) {
	switch incoming := gc.Message().(type) {
	case Begin:
		gc.Response(Completion{
			Err: owner.registry.Begin(incoming.Update),
		})
	case Register:
		gc.Response(Completion{
			Err: owner.registry.Register(incoming.ID, incoming.Candidate),
		})
	case Publish:
		committed, err := owner.registry.Publish(incoming.ID)
		if err == nil {
			topic := gc.ActorSystem().TopicActor()
			if topic == nil {
				err = errors.New("tools: catalog committed but PubSub is unavailable")
			} else {
				event, _ := owner.registry.Read(committed.AgentID)
				err = goakt.Tell(gc.Context(), topic, goakt.NewPublish(uuid.NewString(), tools.DefinitionsTopic, event))
			}
		}
		gc.Response(Catalog{
			Definitions: committed,
			Found:       committed.Version != 0,
			Err:         err,
		})
	case Read:
		committed, found := owner.registry.Read(incoming.AgentID)
		gc.Response(Catalog{
			Definitions: committed,
			Found:       found,
		})
	case Lookup:
		publication, found := owner.registry.Lookup(incoming.ID)
		gc.Response(Outcome{
			Publication: publication,
			Found:       found,
		})
	case Abort:
		gc.Response(Completion{
			Err: owner.registry.Abort(incoming.ID),
		})
	case Settle:
		gc.Response(Completion{
			Err: owner.registry.Settle(incoming.ID),
		})
	default:
		gc.Unhandled()
	}
}
