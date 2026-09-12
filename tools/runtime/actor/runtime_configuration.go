package runtimeactor

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/gorenx/goren/tools"
	toolregistry "github.com/gorenx/goren/tools/registry"
	catalogactor "github.com/gorenx/goren/tools/registry/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

func (owner *Runtime) receiveConfiguration(gc *goakt.GrainContext) bool {
	switch input := gc.Message().(type) {
	case RefreshConfiguration:
		if input.ID == "" ||
			input.CreationID != owner.creationID ||
			owner.collection == nil ||
			owner.request != nil ||
			len(owner.calls) != 0 {
			gc.Response(Definitions{Err: errors.New("tools: configuration requires an idle runtime and current creation")})
			return true
		}
		if owner.connections.changed() {
			owner.changed = true
		}
		if !owner.changed || len(owner.retries) != 0 {
			definitions, err := owner.collection.Definitions()
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
		owner.request = current
		owner.changed = false
		owner.scheduler = nil
		instances := owner.newToolClient(gc)
		owner.schedule(gc, current, func(ctx context.Context) error {
			applied, err := owner.collection.Snapshot()
			if err != nil {
				return err
			}
			return owner.applyCatalog(ctx, applied.Configuration, instances)
		})
	case CancelConfiguration:
		if input.CreationID == owner.creationID &&
			owner.request != nil &&
			owner.request.refreshID == input.ID &&
			owner.request.cancel != nil {
			owner.request.cancel()
		}
		gc.Response(Completion{})
	default:
		return false
	}
	return true
}

func (owner *Runtime) finishRefresh(gc *goakt.GrainContext, current *operation, failure error) {
	if owner.request != current {
		return
	}
	current.cancel()
	owner.request = nil
	if failure != nil {
		owner.changed = true
		current.reply.Response(Definitions{Err: failure})
		return
	}
	definitions, err := owner.collection.Definitions()
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

// applyConfiguration reads the initial Registry selection when no prepared
// configuration was supplied. Later discovery refreshes reuse the applied
// snapshot; they do not adopt Registry configuration changes.
func (owner *Runtime) applyConfiguration(ctx context.Context, directory *catalogactor.Client, instances *toolClient) error {
	configuration, err := directory.Read(ctx, owner.agentID)
	if err != nil {
		return err
	}
	return owner.applyCatalog(ctx, configuration, instances)
}

func (owner *Runtime) applyCatalog(ctx context.Context, configuration toolregistry.Configuration, instances *toolClient) error {
	var err error
	definitions := append(configuration.Global.MCP, configuration.Custom.MCP...)
	if err := owner.connections.reconcile(ctx, definitions); err != nil {
		return err
	}
	configuration.Global, err = owner.connections.catalog(configuration.Global)
	if err != nil {
		return err
	}
	if _, err := owner.collection.Apply(ctx, configuration, instances); err != nil {
		return err
	}
	applied, err := owner.collection.Snapshot()
	if err != nil {
		return err
	}
	return owner.connections.retire(ctx, applied.ProviderIDs)
}
