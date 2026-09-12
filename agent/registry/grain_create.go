package registry

import (
	"context"
	"errors"
	"slices"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/settings"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// activation retains startup input, temporary data and orchestration progress.
// Component ownership lives separately in installation. The first input is frozen.
type activation struct {
	input CreateAgent
	// values records the Settings snapshot used to create this lifetime.
	values    *settings.Values
	component agent.Component
	replies   []*goakt.GrainReply
	cancel    context.CancelFunc
}

// startupResult carries one startup operation's reply or failure. The handler
// validates it before advancing; receipt does not mean the Agent is ready.
type startupResult struct {
	operation *activation
	result    componentResult
}

func (owner *Grain) createChildAgent(gc *goakt.GrainContext, input CreateChildAgent) {
	if input.Child.AgentID == "" || input.Child.CreationID == "" ||
		input.Child.AgentID == input.Parent.AgentID {
		gc.Response(Ready{Err: ErrCreation})
		return
	}
	if err := owner.check(input.Parent); err != nil {
		gc.Response(Ready{Err: err})
		return
	}
	if err := owner.directory.CheckAdmission(input.Parent.AgentID); err != nil {
		gc.Response(Ready{Err: err})
		return
	}
	parent := owner.creations[input.Parent.AgentID]
	if parent.activation == nil || parent.activation.cancel != nil {
		gc.Response(Ready{Err: errors.New("agent registry: parent creation is not ready")})
		return
	}
	derived := parent.activation.input.clone()
	derived.Manager = input.Manager
	derived.Role = input.Role
	derived.Creation = input.Child
	derived.Parent = &input.Parent
	derived.Restore = input.Restore
	derived.Options = input.Options.Clone()
	derived.ConfigurationName = input.ConfigurationName
	if input.ConfigurationName == "" {
		// Children without a named Settings selection retain their existing
		// configuration mechanism instead of implicitly following process defaults.
		derived.Settings = nil
	}
	if input.AgentOptions != nil {
		captured := input.AgentOptions.Clone()
		derived.AgentOptions = &captured
	} else {
		derived.AgentOptions = nil
	}
	if input.Tools != nil {
		catalog := input.Tools.Clone()
		derived.Tools = &catalog
	}
	derived.Prompt = nil
	if input.Prompt != nil {
		captured := input.Prompt.Clone()
		derived.Prompt = &captured
	}
	derived.PromptSections = slices.Clone(input.PromptSections)
	if input.Components != nil {
		derived.Components = slices.Clone(input.Components)
	}
	owner.createAgent(gc, derived)
}

func (owner *Grain) createAgent(gc *goakt.GrainContext, input CreateAgent) {
	if owner.reuseCreation(gc, input) {
		return
	}
	err := input.validate()
	if err != nil {
		gc.Response(Ready{Err: err})
		return
	}
	reply, err := deferLifecycle(gc)
	if err != nil {
		gc.Response(Ready{Err: err})
		return
	}
	err = owner.reserveAgent(ReserveAgent{
		Role:     input.Role,
		Creation: input.Creation,
		Parent:   input.Parent,
	})
	if err != nil {
		reply.Response(Ready{Err: err})
		return
	}
	err = owner.directory.BeginInitialization(input.Creation.AgentID, agent.Requirements{
		Commands:   input.Commands != nil,
		Components: len(input.Components),
	})
	if err != nil {
		_ = owner.directory.FailInitialization(input.Creation.AgentID, err)
		reply.Response(Ready{Err: err})
		owner.scheduleClose(gc, input.Creation, nil)
		return
	}
	operation := &activation{
		input:   input.clone(),
		replies: []*goakt.GrainReply{reply},
	}
	entry := owner.creations[input.Creation.AgentID]
	entry.activation = operation
	owner.creations[input.Creation.AgentID] = entry
	owner.prepareCreation(gc, operation)
}

func (owner *Grain) installCreation(gc *goakt.GrainContext, operation *activation, input CreateAgent) {
	entry := owner.creations[input.Creation.AgentID]
	entry.services = newInstallation(gc.Self(), input, entry.releases)
	owner.creations[input.Creation.AgentID] = entry
	owner.runCreation(gc, operation)
}

func (owner *Grain) runCreation(gc *goakt.GrainContext, operation *activation) {
	identifier := operation.input.Creation.AgentID
	dependency, pending, err := owner.directory.NextComponent(identifier)
	if err != nil || !pending {
		owner.finishCreation(gc, operation, err)
		return
	}
	operation.component = dependency
	entry := owner.creations[identifier]
	endpoint := entry.services.components[dependency]
	if endpoint == nil {
		owner.finishCreation(gc, operation, errors.New("agent registry: component client is missing"))
		return
	}
	err = owner.directory.AcquireComponent(identifier, dependency)
	if err != nil {
		owner.finishCreation(gc, operation, err)
		return
	}
	ctx, cancel := context.WithCancel(context.WithoutCancel(gc.Context()))
	operation.cancel = cancel
	completed := make(chan componentResult, 1)
	err = gc.PipeToSelf(func() (any, error) {
		select {
		case result := <-completed:
			return startupResult{
				operation: operation,
				result:    result,
			}, nil
		case <-ctx.Done():
			return startupResult{
				operation: operation,
				result:    componentResult{err: context.Cause(ctx)},
			}, nil
		}
	})
	if err != nil {
		owner.finishCreation(gc, operation, err)
		return
	}
	endpoint.Initialize(gc, completed)

}

func (owner *Grain) handleStartupResult(gc *goakt.GrainContext, input startupResult) {
	operation := input.operation
	identifier := operation.input.Creation.AgentID
	entry, found := owner.creations[identifier]
	if !found || entry.activation != operation || operation.cancel == nil {
		return
	}
	operation.cancel()
	operation.cancel = nil
	if input.result.unowned {
		owner.directory.RejectComponent(identifier, operation.component)
	}
	failure := input.result.err
	if failure == nil {
		failure = owner.directory.ConfirmComponent(identifier, operation.component)
	}
	if failure != nil {
		owner.finishCreation(gc, operation, failure)
		return
	}
	owner.runCreation(gc, operation)
}

func (owner *Grain) finishCreation(gc *goakt.GrainContext, operation *activation, failure error) {
	if operation.cancel != nil {
		operation.cancel()
		operation.cancel = nil
	}
	identifier := operation.input.Creation.AgentID
	if failure == nil {
		failure = owner.directory.CompleteInitialization(identifier)
	}
	if failure != nil {
		_ = owner.directory.FailInitialization(identifier, failure)
	}
	result := owner.ready(identifier)
	if failure != nil {
		result = Ready{Err: failure}
	}
	for _, reply := range operation.replies {
		reply.Response(result)
	}
	operation.replies = nil
	if failure == nil {
		owner.restartChanged(gc)
	}
	entry := owner.creations[operation.input.Creation.AgentID]
	state, _ := owner.directory.State(identifier)
	if state == agent.Closing {
		owner.scheduleClose(gc, operation.input.Creation, nil)
	} else if entry.closure != nil && entry.closure.cancel == nil {
		owner.runClose(gc, entry.closure)
	}
}

func (owner *Grain) ready(identifier session.SessionID) Ready {
	entry, found := owner.creations[identifier]
	if !found || owner.directory.IsClosed(identifier) {
		return Ready{Err: ErrCreation}
	}
	err := owner.directory.CheckAdmission(identifier)
	if err != nil {
		return Ready{Err: err}
	}
	if entry.activation == nil || entry.activation.cancel != nil {
		return Ready{Err: errors.New("agent registry: creation is not ready")}
	}
	err = owner.directory.CheckReady(identifier)
	if err != nil {
		return Ready{Err: err}
	}
	registered, _ := owner.directory.Get(identifier)
	return Ready{
		Creation: Creation{
			AgentID:    identifier,
			CreationID: entry.creationID,
		},
		Registration: registered,
		Executor:     entry.executor,
		Commands:     entry.services.commandRoute(),
		Components:   entry.services.capabilityRoutes(),
	}
}

// reuseCreation shares initialization or verifies the existing ready endpoint.
func (owner *Grain) reuseCreation(gc *goakt.GrainContext, input CreateAgent) bool {
	previous, found := owner.creations[input.Creation.AgentID]
	if found && !owner.directory.IsClosed(input.Creation.AgentID) {
		if previous.creationID != input.Creation.CreationID ||
			previous.activation == nil ||
			!previous.activation.input.matches(input) {
			gc.Response(Ready{Err: ErrCreation})
			return true
		}
		err := owner.directory.CheckAdmission(input.Creation.AgentID)
		if err != nil {
			gc.Response(Ready{Err: err})
			return true
		}
		current := previous.activation
		if current.cancel == nil {
			owner.findAgent(gc, FindAgent{AgentID: input.Creation.AgentID})
			return true
		}
		reply := gc.DeferResponse()
		if reply == nil {
			gc.Response(Ready{Err: errors.New("agent registry: creation requires a request")})
			return true
		}
		current.replies = append(current.replies, reply)
		return true
	}
	return false
}
