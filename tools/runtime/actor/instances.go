package runtimeactor

import (
	"context"
	"errors"
	"fmt"
	"time"

	agentactor "github.com/gorenx/goren/agent/actor"
	"github.com/gorenx/goren/session"
	toolregistry "github.com/gorenx/goren/tools/registry"
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
	// tools records attempted creation before sending. An empty value means the
	// reply is unconfirmed; a confirmed lifetime remains until removal succeeds.
	tools map[string]agentactor.ToolInstance
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
		tools:      make(map[string]agentactor.ToolInstance),
	}, nil
}

// Prepare confirms a tool lifetime, then constructs and registers its candidate.
func (endpoint *Instances) Prepare(ctx context.Context, revision tool.Revision, entry toolregistry.Tool) error {
	if _, found := endpoint.tools[entry.Name]; !found {
		endpoint.tools[entry.Name] = agentactor.ToolInstance{}
	}
	instance, err := endpoint.confirmCreation(ctx, entry.Name)
	if err != nil {
		return err
	}
	return endpoint.complete(ctx, instance.Identity, toolactor.Provision{
		CreationID: instance.CreationID,
		Revision:   revision,
		Provider:   entry.Provider,
		Visible:    entry.Visible,
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
	instance, err := endpoint.confirmCreation(ctx, name)
	if err != nil {
		return err
	}
	payload, err := endpoint.request(ctx, endpoint.authority, agentactor.RemoveTool{
		AgentID:         endpoint.agentID,
		AgentCreationID: endpoint.creationID,
		Name:            name,
		ToolCreationID:  instance.CreationID,
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
	instance, found := endpoint.tools[name]
	if !found {
		return instance, errors.New("tools: tool creation was not requested")
	}
	if instance.Identity != nil {
		return instance, nil
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
	endpoint.tools[name] = reply
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
