package registry

import (
	"context"
	"errors"
	"reflect"
	"regexp"
	"slices"

	agentactor "github.com/gorenx/goren/agent/actor"
	loopactor "github.com/gorenx/goren/agentloop/actor"
	commandactor "github.com/gorenx/goren/commands/actor"
	llmactor "github.com/gorenx/goren/llm/actor"
	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	sessionregistry "github.com/gorenx/goren/session/registry"
	promptactor "github.com/gorenx/goren/systemprompt/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/reentrancy"
)

type creationStage uint8

const (
	registerSession creationStage = iota
	startSession
	initializePrompt
	readModelHistory
	initializeModel
	activateExecutor
	initializeExecutor
	initializeCommands
	initializeComponents
	creationReady
)

// activation retains only orchestration progress and framework addresses. The
// first input is frozen until this lifetime has been fully closed.
type activation struct {
	input         CreateAgent
	stage         creationStage
	conversation  *goakt.GrainIdentity
	prompt        *goakt.GrainIdentity
	commands      *goakt.GrainIdentity
	components    []Component
	componentNext int
	history       []session.Event
	// sessionRegistered records cleanup responsibility before initialization starts.
	sessionRegistered bool
	// initializing distinguishes an outstanding request from a settled failure.
	initializing bool
	failure      error
	replies      []*goakt.GrainReply
	cancel       context.CancelFunc
}

type creationAdvanced struct {
	operation *activation
	payload   any
	err       error
}

func (owner *Grain) createAgent(gc *goakt.GrainContext, input CreateAgent) {
	if previous, found := owner.creations[input.Creation.AgentID]; found && !previous.closed {
		if previous.creationID != input.Creation.CreationID ||
			previous.activation == nil ||
			!previous.activation.matches(input) {
			gc.Response(Ready{
				Err: ErrCreation,
			})
			return
		}
		if err := owner.directory.CheckAdmission(input.Creation.AgentID); err != nil {
			gc.Response(Ready{
				Err: err,
			})
			return
		}
		current := previous.activation
		if !current.initializing {
			owner.resolve(gc, Resolve{
				AgentID: input.Creation.AgentID,
			})
			return
		}
		reply := gc.DeferResponse()
		if reply == nil {
			gc.Response(Ready{
				Err: errors.New("agent registry: creation requires a request"),
			})
			return
		}
		current.replies = append(current.replies, reply)
		return
	}
	if input.Storage == nil ||
		input.SessionRegistry == nil ||
		input.ModelConfig == nil ||
		input.PromptRegistry == nil ||
		input.ToolsRegistry == nil ||
		input.Timeout <= 0 {
		gc.Response(Ready{
			Err: errors.New("agent registry: complete creation dependencies are required"),
		})
		return
	}
	if !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`).MatchString(string(input.Creation.AgentID)) || len(sessionactor.RuntimeName(string(input.Creation.AgentID))) > 255 {
		gc.Response(Ready{
			Err: errors.New("agent registry: SessionID cannot form a valid runtime address"),
		})
		return
	}
	seen := make(map[string]struct{}, len(input.Components))
	for _, route := range input.Components {
		if route == nil {
			gc.Response(Ready{
				Err: errors.New("agent registry: nil component owner"),
			})
			return
		}
		if err := route.Validate(); err != nil {
			gc.Response(Ready{
				Err: err,
			})
			return
		}
		if _, found := seen[route.String()]; found {
			gc.Response(Ready{
				Err: errors.New("agent registry: duplicate component owner"),
			})
			return
		}
		seen[route.String()] = struct{}{}
	}
	if err := gc.EnableReentrancy(reentrancy.New(reentrancy.WithMode(reentrancy.AllowAll))); err != nil {
		gc.Response(Ready{
			Err: err,
		})
		return
	}
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(Ready{
			Err: errors.New("agent registry: creation requires a request"),
		})
		return
	}
	if err := owner.reserve(Reserve{
		Creation: input.Creation,
		Parent:   input.Parent,
	}); err != nil {
		reply.Response(Ready{
			Err: err,
		})
		return
	}
	input.Options = input.Options.Clone()
	if input.Parent != nil {
		value := *input.Parent
		input.Parent = &value
	}
	input.Components = slices.Clone(input.Components)
	operation := &activation{
		input:        input,
		initializing: true,
		replies:      []*goakt.GrainReply{reply},
	}
	entry := owner.creations[input.Creation.AgentID]
	entry.activation = operation
	owner.creations[input.Creation.AgentID] = entry
	owner.runCreation(gc, operation)
}

func (owner *Grain) runCreation(gc *goakt.GrainContext, operation *activation) {
	input := operation.input
	entry := owner.creations[input.Creation.AgentID]
	var target *goakt.GrainIdentity
	var message any
	switch operation.stage {
	case registerSession:
		target = input.SessionRegistry
		message = sessionregistry.RegisterSession{
			SessionID:  input.Creation.AgentID,
			CreationID: input.Creation.CreationID,
			Restore:    input.Restore,
			Options:    input.Options,
			Storage:    input.Storage,
		}
	case startSession:
		target = input.SessionRegistry
		message = sessionregistry.StartSession{
			SessionID:  input.Creation.AgentID,
			CreationID: input.Creation.CreationID,
		}
	case initializePrompt:
		var err error
		operation.conversation, err = goakt.GrainOf[*sessionactor.Grain](gc.Context(), gc.ActorSystem(), sessionactor.RuntimeName(string(input.Creation.AgentID)), goakt.WithLongLivedGrain())
		if err != nil {
			owner.finishCreation(gc, operation, err)
			return
		}
		target, err = goakt.GrainOf[*promptactor.RuntimeGrain](gc.Context(), gc.ActorSystem(), promptactor.RuntimeName(string(input.Creation.AgentID)), goakt.WithLongLivedGrain())
		operation.prompt = target
		if err != nil {
			owner.finishCreation(gc, operation, err)
			return
		}
		message = promptactor.InitializeRuntime{
			AgentID:        input.Creation.AgentID,
			PromptRegistry: input.PromptRegistry,
			ToolsRegistry:  input.ToolsRegistry,
		}
	case readModelHistory:
		target = operation.conversation
		message = sessionactor.Read{
			CreationID: input.Creation.CreationID,
		}
	case initializeModel:
		target = input.ModelConfig
		message = llmactor.InitializeAgent{
			AgentID: string(input.Creation.AgentID),
			Events:  operation.history,
		}
	case activateExecutor:
		system := gc.ActorSystem()
		ctx, cancel := context.WithCancel(context.WithoutCancel(gc.Context()))
		operation.cancel = cancel
		if err := gc.PipeToSelf(func() (any, error) {
			identity, err := goakt.GrainOf[*loopactor.Grain](ctx, system, loopactor.RuntimeName(string(input.Creation.AgentID)), goakt.WithLongLivedGrain())
			return creationAdvanced{
				operation: operation,
				payload:   identity,
				err:       err,
			}, nil
		}); err != nil {
			owner.finishCreation(gc, operation, err)
		}
		return
	case initializeExecutor:
		target = entry.executor
		message = agentactor.Initialize{
			AgentID:    input.Creation.AgentID,
			Authority:  gc.Self(),
			CreationID: input.Creation.CreationID,
			Session:    operation.conversation,
			Prompt:     operation.prompt,
		}
	case initializeCommands:
		if input.Commands == nil {
			operation.stage++
			owner.runCreation(gc, operation)
			return
		}
		var err error
		target, err = goakt.GrainOf[*commandactor.Grain](gc.Context(), gc.ActorSystem(), commandactor.RuntimeName(string(input.Creation.AgentID)), goakt.WithLongLivedGrain())
		operation.commands = target
		if err != nil {
			owner.finishCreation(gc, operation, err)
			return
		}
		message = commandactor.Initialize{
			SessionID:  input.Creation.AgentID,
			CreationID: input.Creation.CreationID,
			Session:    operation.conversation,
			Registry:   input.Commands,
			Timeout:    input.Timeout,
		}
	case initializeComponents:
		if operation.componentNext == len(input.Components) {
			operation.stage++
			owner.runCreation(gc, operation)
			return
		}
		target = input.Components[operation.componentNext]
		operation.components = append(operation.components, Component{
			Owner: target,
		})
		message = agentactor.StartComponent{
			AgentID:    input.Creation.AgentID,
			CreationID: input.Creation.CreationID,
			Agent:      entry.executor,
			Session:    operation.conversation,
		}
	case creationReady:
		owner.finishCreation(gc, operation, nil)
		return
	}
	ctx, cancel := context.WithCancel(context.WithoutCancel(gc.Context()))
	operation.cancel = cancel
	completed := make(chan creationAdvanced, 1)
	if err := gc.PipeToSelf(func() (any, error) {
		select {
		case result := <-completed:
			return result, nil
		case <-ctx.Done():
			return creationAdvanced{
				operation: operation,
				err:       context.Cause(ctx),
			}, nil
		}
	}); err != nil {
		owner.finishCreation(gc, operation, err)
		return
	}
	gc.RequestGrain(target, message, goakt.WithRequestTimeout(0)).Then(func(payload any, failure error) {
		completed <- creationAdvanced{
			operation: operation,
			payload:   payload,
			err:       failure,
		}
	})
}

func (owner *Grain) advanceCreation(gc *goakt.GrainContext, input creationAdvanced) {
	operation := input.operation
	entry, found := owner.creations[operation.input.Creation.AgentID]
	if !found || entry.activation != operation || !operation.initializing {
		return
	}
	operation.cancel()
	operation.cancel = nil
	failure := input.err
	if failure == nil {
		switch operation.stage {
		case registerSession:
			result, ok := input.payload.(sessionregistry.Completion)
			if !ok {
				failure = errors.New("agent registry: invalid Session registration reply")
			} else {
				failure = result.Err
			}
			operation.sessionRegistered = failure == nil
		case startSession:
			result, ok := input.payload.(sessionactor.Initialized)
			if !ok {
				failure = errors.New("agent registry: invalid Session initialization reply")
			} else {
				failure = result.Err
			}
		case initializePrompt:
			result, ok := input.payload.(promptactor.RuntimeInitialized)
			if !ok {
				failure = errors.New("agent registry: invalid Prompt initialization reply")
			} else {
				failure = result.Err
			}
		case readModelHistory:
			result, ok := input.payload.(sessionactor.View)
			if !ok {
				failure = errors.New("agent registry: invalid model history reply")
			} else {
				failure = result.Err
				operation.history = result.Snapshot.Events
			}
		case initializeModel:
			result, ok := input.payload.(llmactor.Completion)
			if !ok {
				failure = errors.New("agent registry: invalid LLM initialization reply")
			} else {
				failure = result.Err
			}
			operation.history = nil
		case activateExecutor:
			identity, ok := input.payload.(*goakt.GrainIdentity)
			if !ok || identity == nil {
				failure = errors.New("agent registry: missing executor identity")
			} else {
				entry.executor = identity
				owner.creations[operation.input.Creation.AgentID] = entry
			}
		case initializeExecutor:
			result, ok := input.payload.(agentactor.Initialized)
			if !ok {
				failure = errors.New("agent registry: invalid initialization reply")
			} else {
				failure = result.Err
			}
			if failure == nil && entry.instanceID == "" {
				failure = ErrInstance
			}
		case initializeCommands:
			result, ok := input.payload.(commandactor.Completion)
			if !ok {
				failure = errors.New("agent registry: invalid Commands initialization reply")
			} else {
				failure = result.Err
			}
		case initializeComponents:
			result, ok := input.payload.(agentactor.ComponentStarted)
			if !ok {
				failure = errors.New("agent registry: invalid component initialization reply")
			} else {
				failure = result.Err
			}
			if failure == nil && result.Runtime == nil {
				failure = errors.New("agent registry: missing component runtime")
			}
			if failure == nil {
				operation.components[operation.componentNext].Runtime = result.Runtime
			}

		}
	}
	if failure == nil {
		failure = owner.directory.CheckAdmission(operation.input.Creation.AgentID)
	}
	if failure != nil {
		owner.finishCreation(gc, operation, failure)
		return
	}
	if operation.stage == initializeComponents {
		operation.componentNext++
	} else {
		operation.stage++
	}
	owner.runCreation(gc, operation)
}

func (owner *Grain) finishCreation(gc *goakt.GrainContext, operation *activation, failure error) {
	if operation.cancel != nil {
		operation.cancel()
		operation.cancel = nil
	}
	operation.initializing = false
	operation.failure = failure
	result := owner.ready(operation.input.Creation.AgentID)
	for _, reply := range operation.replies {
		reply.Response(result)
	}
	operation.replies = nil
	entry := owner.creations[operation.input.Creation.AgentID]
	if entry.closure != nil && entry.closure.cancel == nil {
		owner.runClose(gc, entry.closure)
	}
}

func (owner *Grain) ready(identifier session.SessionID) Ready {
	entry, found := owner.creations[identifier]
	if !found || entry.closed {
		return Ready{
			Err: ErrCreation,
		}
	}
	if err := owner.directory.CheckAdmission(identifier); err != nil {
		return Ready{
			Err: err,
		}
	}
	if entry.activation == nil || entry.activation.initializing {
		return Ready{
			Err: errors.New("agent registry: creation is not ready"),
		}
	}
	if entry.activation.failure != nil {
		return Ready{
			Err: entry.activation.failure,
		}
	}
	registered, _ := owner.directory.Get(identifier)
	return Ready{
		Creation: Creation{
			AgentID:    identifier,
			CreationID: entry.creationID,
		},
		Registration: registered,
		Executor:     entry.executor,
		Commands:     entry.activation.commands,
		Components:   slices.Clone(entry.activation.components),
	}
}

// matches prevents a repeated creation request from changing frozen configuration.
func (operation *activation) matches(input CreateAgent) bool {
	original := operation.input
	if original.Creation != input.Creation ||
		!reflect.DeepEqual(original.Parent, input.Parent) ||
		original.Restore != input.Restore ||
		!reflect.DeepEqual(original.Options, input.Options) ||
		!reflect.DeepEqual(original.Storage, input.Storage) ||
		original.Timeout != input.Timeout ||
		!sameRoute(original.SessionRegistry, input.SessionRegistry) ||
		!sameRoute(original.ModelConfig, input.ModelConfig) ||
		!sameRoute(original.PromptRegistry, input.PromptRegistry) ||
		!sameRoute(original.ToolsRegistry, input.ToolsRegistry) ||
		!sameRoute(original.Commands, input.Commands) ||
		len(original.Components) != len(input.Components) {
		return false
	}
	for index, route := range original.Components {
		if !sameRoute(route, input.Components[index]) {
			return false
		}
	}
	return true
}

func sameRoute(left, right *goakt.GrainIdentity) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.Equal(right)
}
