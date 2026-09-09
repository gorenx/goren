package runtimeactor

import (
	"context"
	"errors"
	"time"

	"github.com/gorenx/goren/tools/execution"

	agentactor "github.com/gorenx/goren/agent/actor"
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
	bindings := make(map[string]destination, len(snapshot.Names))
	descriptions := make([]tools.Descriptor, 0, len(snapshot.Names))
	for _, name := range snapshot.Names {
		instance, found := endpoint.tools[name]
		if !found || instance.Identity == nil || instance.CreationID == "" {
			return nil, errors.New("tools: execution has no confirmed tool lifetime")
		}
		payload, err := endpoint.request(ctx, instance.Identity, toolactor.Describe{
			CreationID: instance.CreationID,
		})
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
		bindings[name] = destination{
			instance:       instance,
			implementation: description.Implementation,
		}
		descriptions = append(descriptions, description.Descriptor)
	}
	return execution.NewScheduler(
		descriptions,
		policies,
		&bodies{
			system:   endpoint.system,
			bindings: bindings,
		},
	)
}

// bodies contains a detached set of exact destinations, never an Instances owner.
// Waiting ignores caller cancellation so only the ToolActor can confirm body exit.
type bodies struct {
	system   goakt.ActorSystem
	bindings map[string]destination
}

func (endpoint *bodies) Invoke(ctx context.Context, input tools.Invocation) (tools.BodyResult, error) {
	if ctx == nil {
		return tools.BodyResult{}, errors.New("tools: execution context is required")
	}
	target, found := endpoint.bindings[input.Name]
	if !found {
		return tools.BodyResult{}, errors.New("tools: execution destination is missing")
	}
	input.Arguments = input.ArgumentsJSON()
	payload, err := endpoint.system.AskGrain(context.WithoutCancel(ctx), target.instance.Identity, toolactor.Execute{
		CreationID:     target.instance.CreationID,
		Implementation: target.implementation,
		Invocation:     input,
		Context:        ctx,
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

type destination struct {
	instance       agentactor.ToolInstance
	implementation tool.Identity
}
