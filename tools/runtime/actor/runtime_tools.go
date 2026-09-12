package runtimeactor

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/gorenx/goren/tools"
	catalogactor "github.com/gorenx/goren/tools/registry/actor"
	"github.com/gorenx/goren/tools/tool"
	toolactor "github.com/gorenx/goren/tools/tool/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

// These messages belong to the Runtime's Instances adapter, not Agent Registry.
type createTool struct {
	CreationID string
	Name       string
}
type findTool struct {
	CreationID string
	Name       string
}

type removeTool struct {
	CreationID string
	Name       string
}
type toolInstance struct {
	// Ready confirms that initialization and address registration completed.
	Ready      bool
	Identity   *goakt.GrainIdentity
	CreationID string
	Err        error
}

type toolPhase uint8

const (
	// toolReserved owns an attempted activation; no Provision has been admitted.
	toolReserved toolPhase = iota
	// toolInitialized has confirmed identity binding and address registration.
	toolInitialized
	// toolClosing prevents reuse while owned work is being released.
	toolClosing
	// toolReleased permits address withdrawal and stop retries without closing work again.
	toolReleased
)

type toolLifecycle struct {
	creationID string
	instanceID string
	identity   *goakt.GrainIdentity
	phase      toolPhase
	active     *toolRequest
}

type toolRequest struct {
	remove bool
	reply  *goakt.GrainReply
	cancel context.CancelFunc
}

type toolLifecycleResult struct {
	name     string
	instance *toolLifecycle
	request  *toolRequest
	identity *goakt.GrainIdentity
	phase    toolPhase
	err      error
}

func (owner *Runtime) receiveTools(gc *goakt.GrainContext) bool {
	switch input := gc.Message().(type) {
	case createTool:
		owner.createTool(gc, input)
	case findTool:
		if input.CreationID != owner.creationID {
			gc.Response(toolInstance{Err: errors.New("tools: wrong Runtime creation")})
			return true
		}
		instance := owner.tools[input.Name]
		if instance == nil {
			gc.Response(toolInstance{})
			return true
		}
		gc.Response(toolInstance{
			Identity:   instance.identity,
			CreationID: instance.creationID,
			Ready:      instance.phase == toolInitialized,
		})
	case removeTool:
		if input.CreationID != owner.creationID || owner.active == nil {
			gc.Response(Completion{Err: errors.New("tools: removal requires the current Runtime operation")})
			return true
		}
		instance := owner.tools[input.Name]
		if instance == nil {
			gc.Response(Completion{})
			return true
		}
		owner.scheduleTool(gc, input.Name, instance, true)
	case toolactor.Claim:
		gc.Response(toolactor.Completion{Err: owner.claimTool(input)})
	case toolLifecycleResult:
		gc.NoErr()
		if owner.tools[input.name] != input.instance || input.instance.active != input.request {
			return true
		}
		input.request.cancel()
		input.instance.active = nil
		input.instance.identity = input.identity
		input.instance.phase = input.phase
		if input.request.remove {
			if input.err == nil {
				delete(owner.tools, input.name)
			}
			input.request.reply.Response(Completion{Err: input.err})
		} else {
			input.request.reply.Response(toolInstance{
				Ready:      input.phase == toolInitialized,
				Identity:   input.identity,
				CreationID: input.instance.creationID,
				Err:        input.err,
			})
		}
	default:
		return false
	}
	return true
}

func (owner *Runtime) createTool(gc *goakt.GrainContext, input createTool) {
	if input.CreationID != owner.creationID ||
		owner.active == nil ||
		input.Name == "" ||
		strings.TrimSpace(input.Name) != input.Name ||
		input.Name == tools.RunCodeName ||
		len(toolactor.RuntimeName(string(owner.agentID), input.Name)) > 255 {
		gc.Response(toolInstance{Err: errors.New("tools: valid tool name and current Runtime operation are required")})
		return
	}
	instance := owner.tools[input.Name]
	if instance == nil {
		instance = &toolLifecycle{creationID: uuid.NewString()}
		if owner.tools == nil {
			owner.tools = make(map[string]*toolLifecycle)
		}
		owner.tools[input.Name] = instance
	}
	if instance.phase >= toolClosing {
		gc.Response(toolInstance{Err: errors.New("tools: tool lifetime is closing")})
		return
	}
	if instance.phase == toolInitialized && instance.active == nil {
		gc.Response(toolInstance{
			Ready:      true,
			Identity:   instance.identity,
			CreationID: instance.creationID,
		})
		return
	}
	owner.scheduleTool(gc, input.Name, instance, false)
}

func (owner *Runtime) claimTool(input toolactor.Claim) error {
	instance := owner.tools[input.Name]
	if input.AgentCreationID != owner.creationID ||
		input.AgentID != owner.agentID ||
		instance == nil ||
		instance.creationID != input.CreationID ||
		instance.phase >= toolClosing ||
		input.InstanceID == "" {
		return errors.New("tools: tool lifetime is not reserved")
	}
	if instance.instanceID != "" && instance.instanceID != input.InstanceID {
		return errors.New("tools: replacement activation cannot claim the old tool lifetime")
	}
	instance.instanceID = input.InstanceID
	return nil
}

func (owner *Runtime) scheduleTool(gc *goakt.GrainContext, name string, instance *toolLifecycle, removing bool) {
	if instance.active != nil {
		failure := errors.New("tools: tool lifecycle operation is already running")
		if removing {
			gc.Response(Completion{Err: failure})
		} else {
			gc.Response(toolInstance{Err: failure})
		}
		return
	}
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Err(errors.New("tools: tool lifecycle requires an asynchronous request"))
		return
	}
	ctx, cancel := context.WithCancel(context.WithoutCancel(gc.Context()))
	request := &toolRequest{
		remove: removing,
		reply:  reply,
		cancel: cancel,
	}
	instance.active = request
	phase := instance.phase
	if removing {
		instance.phase = toolClosing
	}
	result := toolLifecycleResult{
		name:     name,
		instance: instance,
		request:  request,
		identity: instance.identity,
		phase:    phase,
	}
	creationID := instance.creationID
	agentCreationID := owner.creationID
	agentID := owner.agentID
	authority := gc.Self()
	system := gc.ActorSystem()
	directory := owner.catalog.target
	timeout := owner.instances.timeout
	var providers tool.ProviderSource
	if owner.connections != nil {
		providers = owner.connections
	}
	confirmed := make(chan error, 1)
	if !removing && result.identity == nil {
		address, err := goakt.GrainOf[*toolactor.Grain](
			ctx,
			system,
			toolactor.RuntimeName(string(agentID), name),
			goakt.WithLongLivedGrain(),
			goakt.WithGrainReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))),
		)
		if err != nil {
			cancel()
			instance.active = nil
			reply.Response(toolInstance{Err: err})
			return
		}
		instance.identity = address
		result.identity = address
	}
	target := result.identity
	var command any
	if !removing {
		command = toolactor.Initialize{
			AgentCreationID: agentCreationID,
			CreationID:      creationID,
			AgentID:         agentID,
			Name:            name,
			Authority:       authority,
			Providers:       providers,
		}
	} else if phase == toolInitialized || phase == toolClosing {
		command = toolactor.Close{
			CreationID: creationID,
			Context:    ctx,
		}
	}
	err := gc.PipeToSelf(func() (any, error) {
		// Only detached transport values cross into this task. Lifecycle records
		// remain mailbox-owned, including claims received during Initialize.
		if removing {
			if result.phase == toolReserved {
				result.phase = toolReleased
			}
			if result.phase == toolInitialized || result.phase == toolClosing {
				result.phase = toolClosing
				result.err = <-confirmed
				if result.err != nil {
					return result, nil
				}
				result.phase = toolReleased
			}
			// A reserved tool never received Provision, so failed initialization owns
			// no tool work. Withdraw even an uncertain registration before stopping it.
			payload, failure := system.AskGrain(ctx, directory, catalogactor.UnregisterTool{
				AgentID:    agentID,
				Name:       name,
				CreationID: creationID,
			}, timeout)
			if failure == nil {
				withdrawn, valid := payload.(catalogactor.Completion)
				if !valid {
					failure = errors.New("tools: invalid address withdrawal reply")
				} else {
					failure = withdrawn.Err
				}
			}
			result.err = failure
			if result.err == nil && result.identity != nil {
				result.err = system.TellGrain(ctx, result.identity, new(goakt.PoisonPill))
			}
			return result, nil
		}
		result.err = <-confirmed
		if result.err != nil {
			return result, nil
		}
		payload, failure := system.AskGrain(ctx, directory, catalogactor.RegisterTool{
			AgentID:    agentID,
			Name:       name,
			CreationID: creationID,
			Actor:      result.identity,
		}, timeout)
		if failure == nil {
			registered, valid := payload.(catalogactor.Completion)
			if !valid {
				failure = errors.New("tools: invalid address registration reply")
			} else {
				failure = registered.Err
			}
		}
		result.err = failure
		if result.err == nil {
			result.phase = toolInitialized
		}
		return result, nil
	})
	if err != nil {
		cancel()
		instance.active = nil
		if removing {
			reply.Response(Completion{Err: err})
		} else {
			reply.Response(toolInstance{Err: err})
		}
	}
	if err == nil && command != nil {
		gc.RequestGrain(target, command,
			goakt.WithRequestTimeout(timeout),
			goakt.WithReentrancyMode(reentrancy.AllowAll),
		).Then(func(payload any, failure error) {
			if failure == nil {
				acknowledged, valid := payload.(toolactor.Completion)
				if !valid {
					failure = errors.New("tools: invalid tool lifecycle reply")
				} else {
					failure = acknowledged.Err
				}
			}
			confirmed <- failure
		})
	}
}
