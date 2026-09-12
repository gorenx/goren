package runtimeactor

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gorenx/goren/session"
	toolruntime "github.com/gorenx/goren/tools/runtime"
	"github.com/gorenx/goren/tools/tool"
	toolactor "github.com/gorenx/goren/tools/tool/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// toolClient sends lifecycle requests for one Agent. It owns no
// tool records and is captured only by the task performing those requests.
type toolClient struct {
	system     goakt.ActorSystem
	runtime    *goakt.GrainIdentity
	agentID    session.SessionID
	creationID string
	timeout    time.Duration
}

var _ toolruntime.Instances = (*toolClient)(nil)

// Prepare confirms a tool lifetime, then asks its ToolActor to prepare work for the effective schema.
func (endpoint *toolClient) Prepare(ctx context.Context, revision tool.Revision, entry toolruntime.Tool) error {
	instance, err := endpoint.confirmCreation(ctx, entry.Schema.Name)
	if err != nil {
		return err
	}
	return endpoint.complete(ctx, instance.Identity, toolactor.Provision{
		CreationID: instance.CreationID,
		Revision:   revision,
		Schema:     entry.Schema,
		ProviderID: entry.ProviderID,
		Context:    ctx,
	})
}

// Commit switches the prepared work after Update has prepared every requested tool.
func (endpoint *toolClient) Commit(ctx context.Context, revision tool.Revision, name string) error {
	instance, err := endpoint.confirmCreation(ctx, name)
	if err != nil {
		return err
	}
	return endpoint.complete(ctx, instance.Identity, toolactor.Commit{
		CreationID: instance.CreationID,
		Revision:   revision,
	})
}

// Retire releases the implementation replaced by the committed revision.
func (endpoint *toolClient) Retire(ctx context.Context, revision tool.Revision, name string) error {
	instance, err := endpoint.confirmCreation(ctx, name)
	if err != nil {
		return err
	}
	return endpoint.complete(ctx, instance.Identity, toolactor.Retire{
		CreationID: instance.CreationID,
		Revision:   revision,
		Context:    ctx,
	})
}

// Abort cleans an attempted candidate only after initialization admitted Provision.
func (endpoint *toolClient) Abort(ctx context.Context, revision tool.Revision, name string) error {
	payload, err := endpoint.request(ctx, endpoint.runtime, findTool{
		CreationID: endpoint.creationID,
		Name:       name,
	})
	if err != nil {
		return err
	}
	instance, valid := payload.(toolInstance)
	if !valid {
		return errors.New("tools: invalid lifecycle lookup reply")
	}
	if instance.Err != nil {
		return instance.Err
	}
	if !instance.Ready {
		return nil
	}
	return endpoint.complete(ctx, instance.Identity, toolactor.Abort{
		CreationID: instance.CreationID,
		Revision:   revision,
		Context:    ctx,
	})
}

// Remove retains the exact lifetime on failure, including an uncertain reply.
func (endpoint *toolClient) Remove(ctx context.Context, name string) error {
	payload, err := endpoint.request(ctx, endpoint.runtime, removeTool{
		CreationID: endpoint.creationID,
		Name:       name,
	})
	if err != nil {
		return err
	}
	result, valid := payload.(Completion)
	if !valid {
		return fmt.Errorf("tools: invalid removal reply %T", payload)
	}
	return result.Err
}

func (endpoint *toolClient) confirmCreation(ctx context.Context, name string) (toolInstance, error) {
	payload, err := endpoint.request(ctx, endpoint.runtime, createTool{
		CreationID: endpoint.creationID,
		Name:       name,
	})
	if err != nil {
		return toolInstance{}, err
	}
	result, valid := payload.(toolInstance)
	if !valid {
		return toolInstance{}, fmt.Errorf("tools: invalid creation reply %T", payload)
	}
	return result, result.Err
}

func (endpoint *toolClient) complete(ctx context.Context, target *goakt.GrainIdentity, message any) error {
	payload, err := endpoint.request(ctx, target, message)
	if err != nil {
		return err
	}
	reply, valid := payload.(toolactor.Completion)
	if !valid {
		return fmt.Errorf("tools: invalid tool operation reply %T", payload)
	}
	return reply.Err
}

func (endpoint *toolClient) request(ctx context.Context, target *goakt.GrainIdentity, message any) (any, error) {
	if ctx == nil {
		return nil, errors.New("tools: instance request context is required")
	}
	if ctx.Err() != nil {
		return nil, context.Cause(ctx)
	}
	return endpoint.system.AskGrain(ctx, target, message, endpoint.timeout)
}
