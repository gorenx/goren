package runtimeactor

import (
	"context"
	"errors"
	"time"

	"github.com/gorenx/goren/tools/execution"
	catalogactor "github.com/gorenx/goren/tools/registry/actor"

	"github.com/gorenx/goren/tools"
	toolruntime "github.com/gorenx/goren/tools/runtime"
	"github.com/gorenx/goren/tools/tool"
	toolactor "github.com/gorenx/goren/tools/tool/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Scheduler captures only confirmed tool lifetimes and their settled descriptors.
// The caller drains every execution before changing or closing those lifetimes.
func (endpoint *Instances) Scheduler(
	ctx context.Context,
	snapshot toolruntime.Snapshot,
	policies execution.Policies,
) (*execution.Scheduler, error) {
	// revisions maps tool names to the committed revisions described below;
	// execution must still target that work after policy preparation.
	revisions := make(map[string]tool.Revision, len(snapshot.Names))
	descriptions := make([]tools.Descriptor, 0, len(snapshot.Names))
	for _, name := range snapshot.Names {
		payload, err := endpoint.request(ctx, endpoint.directory, catalogactor.FindTool{
			AgentID: endpoint.agentID,
			Name:    name,
		})
		if err != nil {
			return nil, err
		}
		address, valid := payload.(catalogactor.ToolAddress)
		if !valid || !address.Found || address.Actor == nil {
			return nil, errors.New("tools: execution has no registered ToolActor")
		}
		payload, err = endpoint.request(ctx, address.Actor, toolactor.Describe{CreationID: address.CreationID})
		if err != nil {
			return nil, err
		}
		description, valid := payload.(toolactor.Description)
		if !valid {
			return nil, errors.New("tools: invalid execution descriptor reply")
		}
		if description.Err != nil {
			return nil, description.Err
		}
		if description.Descriptor.Name != name {
			return nil, errors.New("tools: execution descriptor name does not match its lifetime")
		}
		revisions[name] = description.Revision
		descriptions = append(descriptions, description.Descriptor)
	}
	return execution.NewScheduler(
		descriptions,
		policies,
		&bodies{
			system:    endpoint.system,
			registry:  endpoint.directory,
			timeout:   endpoint.timeout,
			revisions: revisions,
		},
	)
}

// bodies retains prepared revisions, never Actor addresses.
// Each invocation resolves its name through Registry; Execute rejects replacement
// revisions that differ from the description used for policy preparation.
// Waiting ignores caller cancellation so only the ToolActor can confirm body exit.
type bodies struct {
	system   goakt.ActorSystem
	registry *goakt.GrainIdentity
	timeout  time.Duration
	// revisions maps each prepared tool name to its committed revision.
	// The revision is checked during Execute so a replacement cannot receive a call
	// prepared against the old descriptor; this map does not cache Actor addresses.
	revisions map[string]tool.Revision
}

func (endpoint *bodies) Invoke(ctx context.Context, input tools.Invocation) (tools.BodyResult, error) {
	if ctx == nil {
		return tools.BodyResult{}, errors.New("tools: execution context is required")
	}
	selected, found := endpoint.revisions[input.Name]
	if !found {
		return tools.BodyResult{}, errors.New("tools: execution destination is missing")
	}
	payload, err := endpoint.system.AskGrain(ctx, endpoint.registry, catalogactor.FindTool{
		AgentID: input.SessionID,
		Name:    input.Name,
	}, endpoint.timeout)
	if err != nil {
		return tools.BodyResult{}, err
	}
	address, valid := payload.(catalogactor.ToolAddress)
	if !valid || !address.Found || address.Actor == nil {
		return tools.BodyResult{}, errors.New("tools: named ToolActor is not registered")
	}
	input.Arguments = input.ArgumentsJSON()
	payload, err = endpoint.system.AskGrain(context.WithoutCancel(ctx), address.Actor, toolactor.Execute{
		CreationID: address.CreationID,
		Revision:   selected,
		Invocation: input,
		Context:    ctx,
	}, time.Duration(1<<63-1))
	if err != nil {
		return tools.BodyResult{}, err
	}
	result, valid := payload.(tools.BodyResult)
	if !valid {
		return tools.BodyResult{}, errors.New("tools: invalid body reply; execution completion is unconfirmed")
	}
	return result, nil
}
