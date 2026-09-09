package registry

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorenx/goren/agent"
	agentactor "github.com/gorenx/goren/agent/actor"
	"github.com/gorenx/goren/tools"
	catalogactor "github.com/gorenx/goren/tools/registry/actor"
	toolactor "github.com/gorenx/goren/tools/tool/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

type toolPhase uint8

const (
	// toolReserved retains a grant even if framework activation or initialization failed.
	toolReserved toolPhase = iota
	// toolInitialized confirms identity binding, not implementation readiness.
	toolInitialized
	// toolClosing rejects reuse while resource cleanup is pending or has failed.
	toolClosing
	// toolReleased permits deactivation retries without closing a fresh activation.
	toolReleased
)

type toolLifecycle struct {
	input      agentactor.CreateTool
	creationID string
	instanceID string
	identity   *goakt.GrainIdentity
	phase      toolPhase
	active     *toolRequest
}

type toolRequest struct {
	remove  bool
	replies []*goakt.GrainReply
	cancel  context.CancelFunc
}

type toolCreationResult struct {
	instance *toolLifecycle
	request  *toolRequest
	identity *goakt.GrainIdentity
	phase    toolPhase
	err      error
}

// toolRemovalResult reports cleanup progress even when resource release fails.
type toolRemovalResult struct {
	instance *toolLifecycle
	request  *toolRequest
	identity *goakt.GrainIdentity
	phase    toolPhase
	err      error
}

func (owner *Grain) receiveTools(gc *goakt.GrainContext) bool {
	switch input := gc.Message().(type) {
	case agentactor.CreateTool:
		owner.createTool(gc, input)
	case agentactor.RemoveTool:
		owner.removeTool(gc, input)
	case agentactor.ClaimTool:
		gc.Response(agentactor.Claimed{Err: owner.claimTool(input)})
	case toolCreationResult:
		owner.completeToolCreation(gc, input)
	case toolRemovalResult:
		owner.completeToolRemoval(gc, input)
	default:
		return false
	}
	return true
}

func (owner *Grain) createTool(gc *goakt.GrainContext, input agentactor.CreateTool) {
	err := owner.check(Creation{
		AgentID:    input.AgentID,
		CreationID: input.AgentCreationID,
	})
	if err == nil {
		err = owner.directory.CheckAdmission(input.AgentID)
	}
	if err == nil && (input.Name == "" || input.Registry == nil || input.Timeout <= 0) {
		err = errors.New("agent registry: tool name, catalog and positive timeout are required")
	}
	if err == nil && (strings.TrimSpace(input.Name) != input.Name ||
		input.Name == tools.RunCodeName ||
		len(toolactor.RuntimeName(string(input.AgentID), input.Name)) > 255) {
		err = errors.New("agent registry: invalid tool name or framework address")
	}
	if err != nil {
		gc.Response(agentactor.ToolInstance{Err: err})
		return
	}
	entry := owner.creations[input.AgentID]
	instance := entry.tools[input.Name]
	if instance != nil && (instance.input.Registry.String() != input.Registry.String() || instance.phase >= toolClosing) {
		gc.Response(agentactor.ToolInstance{Err: errors.New("agent registry: tool has a conflicting catalog or is closing")})
		return
	}
	if instance == nil {
		instance = &toolLifecycle{
			input:      input,
			creationID: uuid.NewString(),
		}
		if entry.tools == nil {
			entry.tools = make(map[string]*toolLifecycle)
		}
		err := owner.directory.TrackResource(input.AgentID, agent.Resource{
			Kind: agent.Tool,
			Name: input.Name,
		})
		if err != nil {
			gc.Response(agentactor.ToolInstance{Err: err})
			return
		}
		entry.releases[agent.Resource{
			Kind: agent.Tool,
			Name: input.Name,
		}] = &toolClient{input: agentactor.RemoveTool{
			AgentID:         input.AgentID,
			AgentCreationID: input.AgentCreationID,
			Name:            input.Name,
			ToolCreationID:  instance.creationID,
		}}
		entry.tools[input.Name] = instance
		owner.creations[input.AgentID] = entry
	}
	if instance.phase == toolInitialized && instance.active == nil {
		gc.Response(agentactor.ToolInstance{
			Identity:   instance.identity,
			CreationID: instance.creationID,
		})
		return
	}
	owner.startToolRequest(gc, instance, false)
}

func (owner *Grain) removeTool(gc *goakt.GrainContext, input agentactor.RemoveTool) {
	err := owner.check(Creation{
		AgentID:    input.AgentID,
		CreationID: input.AgentCreationID,
	})
	if err != nil {
		gc.Response(agentactor.ToolRemoved{Err: err})
		return
	}
	instance := owner.creations[input.AgentID].tools[input.Name]
	if instance == nil {
		gc.Response(agentactor.ToolRemoved{})
		return
	}
	if input.ToolCreationID != instance.creationID {
		gc.Response(agentactor.ToolRemoved{Err: ErrCreation})
		return
	}
	owner.startToolRequest(gc, instance, true)
}

func (owner *Grain) claimTool(input agentactor.ClaimTool) error {
	entry, found := owner.creations[input.AgentID]
	if !found || owner.directory.IsClosed(input.AgentID) || input.InstanceID == "" {
		return ErrCreation
	}
	instance := entry.tools[input.Name]
	if instance == nil || instance.creationID != input.CreationID || instance.phase == toolReleased {
		return ErrCreation
	}
	if instance.instanceID != "" && instance.instanceID != input.InstanceID {
		return ErrInstance
	}
	// A current grant remains claimable during Agent close so uncertain startup
	// can be confirmed and cleaned. It does not admit new tool creation or calls.
	instance.instanceID = input.InstanceID
	return nil
}

func (owner *Grain) startToolRequest(gc *goakt.GrainContext, instance *toolLifecycle, remove bool) {
	err := gc.EnableReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll)))
	if err != nil {
		owner.replyToolError(gc, remove, err)
		return
	}
	if instance.active != nil && instance.active.remove != remove {
		owner.replyToolError(gc, remove, errors.New("agent registry: tool lifecycle operation is still running"))
		return
	}
	reply := gc.DeferResponse()
	if reply == nil {
		owner.replyToolError(gc, remove, errors.New("agent registry: tool lifecycle requires a request"))
		return
	}
	if instance.active != nil {
		instance.active.replies = append(instance.active.replies, reply)
		return
	}
	ctx, cancel := context.WithCancel(context.WithoutCancel(gc.Context()))
	request := &toolRequest{
		remove:  remove,
		replies: []*goakt.GrainReply{reply},
		cancel:  cancel,
	}
	instance.active = request
	if remove && instance.phase == toolInitialized {
		instance.phase = toolClosing
	}
	// Tasks capture values, never mutate the mailbox-owned lifecycle record.
	input := instance.input
	creationID := instance.creationID
	identity := instance.identity
	phase := instance.phase
	system := gc.ActorSystem()
	authority := gc.Self()
	err = gc.PipeToSelf(func() (any, error) {
		if remove {
			result := removeToolInstance(ctx, system, authority, input, creationID, identity, phase)
			result.instance = instance
			result.request = request
			return result, nil
		}
		result := createToolInstance(ctx, system, authority, input, creationID, identity, phase)
		result.instance = instance
		result.request = request
		return result, nil
	})
	if err != nil {
		cancel()
		instance.active = nil
		request.respond(instance, err)
	}
}

func createToolInstance(ctx context.Context, system goakt.ActorSystem, authority *goakt.GrainIdentity, input agentactor.CreateTool, creationID string, identity *goakt.GrainIdentity, phase toolPhase) toolCreationResult {
	result := toolCreationResult{
		identity: identity,
		phase:    phase,
	}
	if result.identity == nil {
		result.identity, result.err = goakt.GrainOf[*toolactor.Grain](ctx, system, toolactor.RuntimeName(string(input.AgentID), input.Name),
			goakt.WithLongLivedGrain(),
			goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))))
		if result.err != nil {
			return result
		}
	}
	if result.phase == toolReserved {
		result.err = requestTool(ctx, system, result.identity, toolactor.Initialize{
			CreationID: creationID,
			AgentID:    input.AgentID,
			Name:       input.Name,
			Authority:  authority,
		}, input.Timeout)
		if result.err == nil {
			result.err = registerToolAddress(ctx, system, input.Registry, catalogactor.RegisterTool{
				AgentID:    input.AgentID,
				Name:       input.Name,
				CreationID: creationID,
				Actor:      result.identity,
			}, input.Timeout)
		}
		if result.err == nil {
			result.phase = toolInitialized
		}
	}
	return result
}

func removeToolInstance(ctx context.Context, system goakt.ActorSystem, authority *goakt.GrainIdentity, input agentactor.CreateTool, creationID string, identity *goakt.GrainIdentity, phase toolPhase) toolRemovalResult {
	result := toolRemovalResult{
		identity: identity,
		phase:    phase,
	}
	if identity == nil {
		// Initialization was never submitted, so no tool resources were acquired.
		return result
	}
	if phase == toolReserved {
		initialized := createToolInstance(ctx, system, authority, input, creationID, identity, phase)
		result.err = initialized.err
		result.phase = initialized.phase
		if result.err != nil {
			return result
		}
	}
	if result.phase != toolReleased {
		result.phase = toolClosing
		result.err = requestTool(ctx, system, identity, toolactor.Close{
			CreationID: creationID,
			Context:    ctx,
		}, input.Timeout)
		if result.err != nil {
			return result
		}
		result.phase = toolReleased
	}
	result.err = registerToolAddress(ctx, system, input.Registry, catalogactor.UnregisterTool{
		AgentID:    input.AgentID,
		Name:       input.Name,
		CreationID: creationID,
	}, input.Timeout)
	if result.err != nil {
		return result
	}
	result.err = system.TellGrain(ctx, identity, new(goakt.PoisonPill))
	return result
}

func requestTool(ctx context.Context, system goakt.ActorSystem, target *goakt.GrainIdentity, message any, timeout time.Duration) error {
	payload, err := system.AskGrain(ctx, target, message, timeout)
	if err != nil {
		return err
	}
	reply, valid := payload.(toolactor.Completion)
	if !valid {
		return errors.New("agent registry: invalid tool lifecycle reply")
	}
	return reply.Err
}

func (owner *Grain) completeToolCreation(gc *goakt.GrainContext, result toolCreationResult) {
	instance := result.instance
	if !owner.acceptToolResult(instance, result.request) {
		return
	}
	instance.identity = result.identity
	instance.phase = result.phase
	if result.err == nil {
		result.err = owner.directory.CheckAdmission(instance.input.AgentID)
	}
	owner.finishToolRequest(gc, instance, result.request, result.err)
}

func (owner *Grain) completeToolRemoval(gc *goakt.GrainContext, result toolRemovalResult) {
	instance := result.instance
	if !owner.acceptToolResult(instance, result.request) {
		return
	}
	instance.identity = result.identity
	instance.phase = result.phase
	if result.err == nil {
		delete(owner.creations[instance.input.AgentID].tools, instance.input.Name)
		owner.directory.ReleaseTool(instance.input.AgentID, instance.input.Name)
	}
	owner.finishToolRequest(gc, instance, result.request, result.err)
}

func (owner *Grain) acceptToolResult(instance *toolLifecycle, request *toolRequest) bool {
	entry, found := owner.creations[instance.input.AgentID]
	return found && entry.tools[instance.input.Name] == instance && instance.active == request
}

func (owner *Grain) finishToolRequest(gc *goakt.GrainContext, instance *toolLifecycle, request *toolRequest, err error) {
	request.cancel()
	instance.active = nil
	request.respond(instance, err)
	entry := owner.creations[instance.input.AgentID]
	if entry.closure != nil && entry.closure.cancel == nil {
		owner.runClose(gc, entry.closure)
	}
}

func (*Grain) replyToolError(gc *goakt.GrainContext, remove bool, err error) {
	if remove {
		gc.Response(agentactor.ToolRemoved{Err: err})
	} else {
		gc.Response(agentactor.ToolInstance{Err: err})
	}
}

func (request *toolRequest) respond(instance *toolLifecycle, err error) {
	for _, reply := range request.replies {
		if request.remove {
			reply.Response(agentactor.ToolRemoved{Err: err})
		} else if err != nil {
			reply.Response(agentactor.ToolInstance{Err: err})
		} else {
			reply.Response(agentactor.ToolInstance{
				Identity:   instance.identity,
				CreationID: instance.creationID,
			})
		}
	}
	request.replies = nil
}

// registerToolAddress sends explicit lifecycle registration or withdrawal.
// Retrying uses the same tool creation, so it cannot remove another lifetime.
func registerToolAddress(ctx context.Context, system goakt.ActorSystem, target *goakt.GrainIdentity, message any, timeout time.Duration) error {
	payload, err := system.AskGrain(ctx, target, message, timeout)
	if err != nil {
		return err
	}
	reply, valid := payload.(catalogactor.Completion)
	if !valid {
		return errors.New("agent registry: invalid tool address registration reply")
	}
	return reply.Err
}
