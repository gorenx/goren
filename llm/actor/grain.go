package llmactor

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/llm/modelconfig"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Grain schedules registration, model definitions and retained Agent settings.
// The independent business objects own validation and configuration rules.
// Keep it active for the application lifetime; passivation does not persist registrations.
type Grain struct {
	registry llm.Registry
	settings llm.Configuration
	catalog  *modelconfig.Catalog
}

var ErrModelsUnavailable = errors.New("llm: model definitions are unavailable")

// OnActivate uses the registry's ready zero value.
func (*Grain) OnActivate(context.Context, *goakt.GrainProps) error { return nil }

// OnDeactivate owns no provider I/O resources; live workers retain selected bindings.
func (*Grain) OnDeactivate(context.Context, *goakt.GrainProps) error { return nil }

// OnReceive delegates business invariants to Registry and publishes only mutations.
func (owner *Grain) OnReceive(gc *goakt.GrainContext) {
	switch input := gc.Message().(type) {
	case InitializeModels:
		if owner.catalog != nil || input.Catalog == nil {
			gc.Response(Completion{
				Err: ErrModelsUnavailable,
			})
			return
		}
		owner.catalog = input.Catalog
		gc.Response(Completion{})
	case ReadModels:
		if owner.catalog == nil {
			gc.Response(Models{
				Err: ErrModelsUnavailable,
			})
			return
		}
		snapshot, err := owner.catalog.Read(input.Version)
		gc.Response(Models{
			Snapshot: snapshot,
			Err:      err,
		})
	case ResolveAgent:
		if owner.catalog == nil {
			gc.Response(AgentConfiguration{
				Err: ErrModelsUnavailable,
			})
			return
		}
		resolved, err := owner.catalog.Resolve(input.Version, input.Overrides)
		gc.Response(AgentConfiguration{
			Resolved: resolved,
			Err:      err,
		})
	case ReplaceModels:
		if owner.catalog == nil {
			gc.Response(Models{
				Err: ErrModelsUnavailable,
			})
			return
		}
		snapshot, err := owner.catalog.Replace(input.ExpectedVersion, input.Definition)
		gc.Response(Models{
			Snapshot: snapshot,
			Err:      err,
		})
	case SetConfiguration:
		gc.Response(ConfigurationResult{
			Err: owner.settings.Set(input.AgentID, input.Config),
		})
	case ReadConfiguration:
		stored, err := owner.settings.Get(input.AgentID)
		gc.Response(ConfigurationResult{
			Config: stored,
			Err:    err,
		})
	case RemoveConfiguration:
		owner.settings.Remove(input.AgentID)
		gc.Response(ConfigurationResult{})
	case ReadCall:
		settings, err := owner.settings.Get(input.AgentID)
		var selected *llm.Binding
		if err == nil {
			selected, err = owner.registry.Select(settings.Provider)
			var failure *llm.LlmError
			if errors.As(err, &failure) && failure.Code() == "NO_ADAPTER" {
				err = nil
			}
		}
		gc.Response(CallSnapshot{
			Config:  settings,
			Binding: selected,
			Err:     err,
		})
	case RegisterAdapter:
		id, err := owner.registry.RegisterAdapter(input.Providers, input.Adapter)
		gc.Response(Registered{
			ID:  id,
			Err: owner.publish(gc, id != "", err),
		})
	case ReplaceAdapter:
		err := owner.registry.ReplaceAdapter(input.ID, input.Providers)
		gc.Response(Completion{
			Changed: err == nil,
			Err:     owner.publish(gc, err == nil, err),
		})
	case RemoveAdapter:
		changed := owner.registry.RemoveAdapter(input.ID)
		gc.Response(Completion{
			Changed: changed,
			Err:     owner.publish(gc, changed, nil),
		})
	case RegisterDirectory:
		id, err := owner.registry.RegisterConfigurableProviders(input.Entries)
		gc.Response(Registered{
			ID:  id,
			Err: owner.publish(gc, id != "", err),
		})
	case ReplaceDirectory:
		err := owner.registry.ReplaceConfigurableProviders(input.ID, input.Entries)
		gc.Response(Completion{
			Changed: err == nil,
			Err:     owner.publish(gc, err == nil, err),
		})
	case RemoveDirectory:
		changed := owner.registry.RemoveConfigurableProviders(input.ID)
		gc.Response(Completion{
			Changed: changed,
			Err:     owner.publish(gc, changed, nil),
		})
	case Select:
		selected, err := owner.registry.Select(input.Provider)
		gc.Response(Selected{
			Binding: selected,
			Err:     err,
		})
	case ReadCatalog:
		gc.Response(Catalog{
			Providers:    owner.registry.ListProviders(),
			Configurable: owner.registry.ListConfigurableProviders(),
		})
	default:
		gc.Unhandled()
	}
}

func (*Grain) publish(gc *goakt.GrainContext, changed bool, failure error) error {
	if failure != nil || !changed {
		return failure
	}
	topic := gc.ActorSystem().TopicActor()
	if topic == nil {
		return errors.New("llm: registration committed but PubSub is unavailable")
	}
	return goakt.Tell(gc.Context(), topic, goakt.NewPublish(uuid.NewString(), llm.AdaptersUpdatedEventName, llm.AdaptersUpdated{}))
}
