package runtimeactor

import (
	"context"
	"errors"
	"time"

	"github.com/gorenx/goren/session"

	catalogactor "github.com/gorenx/goren/tools/registry/actor"

	"github.com/gorenx/goren/tools"
	"github.com/gorenx/goren/tools/tool"
	toolactor "github.com/gorenx/goren/tools/tool/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// bodies retains prepared revisions, never Actor addresses.
// Each invocation resolves its name through Registry; Execute rejects replacement
// revisions that differ from the description used for policy preparation.
// Waiting ignores caller cancellation so only the ToolActor can confirm body exit.
type bodies struct {
	system   goakt.ActorSystem
	agentID  session.SessionID
	registry *goakt.GrainIdentity
	timeout  time.Duration
	// revisions maps each prepared tool name to its committed revision.
	// The revision is checked during Execute so a replacement cannot receive a call
	// prepared against the old descriptor; this map does not cache Actor addresses.
	revisions map[string]tool.Revision
}

// describe captures committed revisions for later invocation. The caller compiles
// these descriptors and owns their lifetime; this adapter does not cache them.
func (endpoint *bodies) describe(ctx context.Context, names []string) ([]tools.Descriptor, error) {
	// revisions maps tool names to the committed revisions described below;
	// execution must still target that work after policy preparation.
	revisions := make(map[string]tool.Revision, len(names))
	descriptions := make([]tools.Descriptor, 0, len(names))
	for _, name := range names {
		payload, err := endpoint.system.AskGrain(ctx, endpoint.registry, catalogactor.FindTool{
			AgentID: endpoint.agentID,
			Name:    name,
		}, endpoint.timeout)
		if err != nil {
			return nil, err
		}
		address, valid := payload.(catalogactor.ToolAddress)
		if !valid || !address.Found || address.Actor == nil {
			return nil, errors.New("tools: execution has no registered ToolActor")
		}
		payload, err = endpoint.system.AskGrain(ctx, address.Actor, toolactor.Describe{CreationID: address.CreationID}, endpoint.timeout)
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
	endpoint.revisions = revisions
	return descriptions, nil
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
