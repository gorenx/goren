package runtimeactor

import (
	"context"
	"errors"
	"fmt"
	"time"

	agentactor "github.com/gorenx/goren/agent/actor"
	"github.com/gorenx/goren/session"
	toolruntime "github.com/gorenx/goren/tools/runtime"
	"github.com/gorenx/goren/tools/tool"
	toolactor "github.com/gorenx/goren/tools/tool/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Instances binds one Agent's update task to its lifecycle and tool owners.
// Only the serial update task may access it; never call it from Actor Receive.
type Instances struct {
	system     goakt.ActorSystem
	authority  *goakt.GrainIdentity
	directory  *goakt.GrainIdentity
	agentID    session.SessionID
	creationID string
	timeout    time.Duration
	// tools maps a canonical tool name to the ToolActor CreationID whose cleanup
	// this Agent still owes, e.g. "read_file" -> "tool-creation-1". An empty value
	// means creation was attempted but its reply is unconfirmed; removal must first
	// confirm that lifetime. Addresses are queried, never cached in this map.
	tools map[string]string
	// workers maps tool names to borrowed constructors supplied by composition.
	// It is separate from schemas and policies and does not contain live resources.
	workers tool.Workers
}

var _ toolruntime.Instances = (*Instances)(nil)

// NewInstances binds existing owners without starting Actors or opening tools.
func NewInstances(
	system goakt.ActorSystem,
	authority *goakt.GrainIdentity,
	directory *goakt.GrainIdentity,
	agentID session.SessionID,
	creationID string,
	timeout time.Duration,
	constructors tool.Workers,
) (*Instances, error) {
	if system == nil || authority == nil || directory == nil ||
		agentID == "" || creationID == "" || timeout <= 0 {
		return nil, errors.New("tools: instance owners, Agent creation and positive timeout are required")
	}
	return &Instances{
		system:     system,
		authority:  authority,
		directory:  directory,
		agentID:    agentID,
		creationID: creationID,
		timeout:    timeout,
		tools:      make(map[string]string),
		workers:    constructors.Clone(),
	}, nil
}

// Prepare confirms a tool lifetime, then asks its ToolActor to prepare work for the effective schema.
func (endpoint *Instances) Prepare(ctx context.Context, revision tool.Revision, entry toolruntime.Tool) error {
	if _, found := endpoint.tools[entry.Schema.Name]; !found {
		endpoint.tools[entry.Schema.Name] = ""
	}
	instance, err := endpoint.confirmCreation(ctx, entry.Schema.Name)
	if err != nil {
		return err
	}
	return endpoint.complete(ctx, instance.Identity, toolactor.Provision{
		CreationID: instance.CreationID,
		Revision:   revision,
		Provider:   endpoint.workers[entry.Schema.Name],
		Schema:     entry.Schema,
		Context:    ctx,
	})
}

// Commit switches the prepared tool after publication is confirmed by Update.
func (endpoint *Instances) Commit(ctx context.Context, revision tool.Revision, name string) error {
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
func (endpoint *Instances) Retire(ctx context.Context, revision tool.Revision, name string) error {
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

// Abort cleans an attempted candidate, confirming an uncertain creation first.
func (endpoint *Instances) Abort(ctx context.Context, revision tool.Revision, name string) error {
	if _, found := endpoint.tools[name]; !found {
		return nil
	}
	instance, err := endpoint.confirmCreation(ctx, name)
	if err != nil {
		return err
	}
	return endpoint.complete(ctx, instance.Identity, toolactor.Abort{
		CreationID: instance.CreationID,
		Revision:   revision,
		Context:    ctx,
	})
}

// Remove retains the exact lifetime on failure, including an uncertain reply.
func (endpoint *Instances) Remove(ctx context.Context, name string) error {
	if _, found := endpoint.tools[name]; !found {
		return nil
	}
	identifier := endpoint.tools[name]
	if identifier == "" {
		instance, err := endpoint.confirmCreation(ctx, name)
		if err != nil {
			return err
		}
		identifier = instance.CreationID
	}
	payload, err := endpoint.request(ctx, endpoint.authority, agentactor.RemoveTool{
		AgentID:         endpoint.agentID,
		AgentCreationID: endpoint.creationID,
		Name:            name,
		ToolCreationID:  identifier,
	})
	if err != nil {
		return err
	}
	reply, valid := payload.(agentactor.ToolRemoved)
	if !valid {
		return fmt.Errorf("tools: invalid removal reply %T", payload)
	}
	if reply.Err != nil {
		return reply.Err
	}
	delete(endpoint.tools, name)
	return nil
}

func (endpoint *Instances) confirmCreation(ctx context.Context, name string) (agentactor.ToolInstance, error) {
	_, found := endpoint.tools[name]
	var instance agentactor.ToolInstance
	if !found {
		return instance, errors.New("tools: tool creation was not requested")
	}
	payload, err := endpoint.request(ctx, endpoint.authority, agentactor.CreateTool{
		AgentID:         endpoint.agentID,
		AgentCreationID: endpoint.creationID,
		Name:            name,
		Registry:        endpoint.directory,
		Timeout:         endpoint.timeout,
	})
	if err != nil {
		return instance, err
	}
	reply, valid := payload.(agentactor.ToolInstance)
	if !valid {
		return instance, fmt.Errorf("tools: invalid creation reply %T", payload)
	}
	if reply.Err != nil {
		return instance, reply.Err
	}
	if reply.Identity == nil || reply.CreationID == "" {
		return instance, errors.New("tools: creation reply contains no tool lifetime")
	}
	endpoint.tools[name] = reply.CreationID
	return reply, nil
}

func (endpoint *Instances) complete(ctx context.Context, target *goakt.GrainIdentity, message any) error {
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

func (endpoint *Instances) request(ctx context.Context, target *goakt.GrainIdentity, message any) (any, error) {
	if ctx == nil {
		return nil, errors.New("tools: instance request context is required")
	}
	if ctx.Err() != nil {
		return nil, context.Cause(ctx)
	}
	return endpoint.system.AskGrain(ctx, target, message, endpoint.timeout)
}
