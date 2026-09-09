package toolactor

import (
	"context"

	"github.com/gorenx/goren/tools"
	toolregistry "github.com/gorenx/goren/tools/registry"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// RegistryGrain serializes catalog transactions and publishes committed definitions.
// It must be long-lived: passivation is not a business resource-release protocol.
type RegistryGrain struct {
	registry toolregistry.Registry
}

// OnActivate creates no resources; the business Registry has a usable zero value.
func (*RegistryGrain) OnActivate(context.Context, *goakt.GrainProps) error { return nil }

// OnDeactivate has no external resources to release.
func (*RegistryGrain) OnDeactivate(context.Context, *goakt.GrainProps) error { return nil }

// OnReceive executes short catalog commands without blocking on other Actors.
func (owner *RegistryGrain) OnReceive(gc *goakt.GrainContext) {
	if owner.receiveConfiguration(gc) {
		return
	}
	switch incoming := gc.Message().(type) {
	case BeginUpdate:
		gc.Response(Completion{
			Err: owner.registry.Begin(incoming.Update),
		})
	case RegisterDefinition:
		gc.Response(Completion{
			Err: owner.registry.Register(incoming.ID, incoming.Candidate),
		})
	case PublishDefinitions:
		committed, err := owner.registry.Publish(incoming.ID)
		found := err == nil
		if found {
			event, _ := owner.registry.Read(committed.AgentID)
			err = owner.publishEvent(gc, tools.DefinitionsTopic, event)
		}
		gc.Response(Catalog{
			Definitions: committed,
			Found:       found,
			Err:         err,
		})
	case ReadDefinitions:
		committed, found := owner.registry.Read(incoming.AgentID)
		gc.Response(Catalog{
			Definitions: committed,
			Found:       found,
		})
	case ReadPublication:
		publication, found := owner.registry.Lookup(incoming.ID)
		gc.Response(Outcome{
			Publication: publication,
			Found:       found,
		})
	case AbortUpdate:
		gc.Response(Completion{
			Err: owner.registry.Abort(incoming.ID),
		})
	case SettleUpdate:
		gc.Response(Completion{
			Err: owner.registry.Settle(incoming.ID),
		})
	default:
		gc.Unhandled()
	}
}
