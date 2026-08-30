package agentloop

import (
	"context"
	"errors"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	sesspersist "github.com/gorenx/goren/session/persistence"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/tools"
)

// Plugin is the sole module adapter for AgentLoop. It publishes one Factory
// and owns no Agent, Registry membership, or parent-child relation.
type Plugin struct {
	plugin.Base
	factory *Factory
	mutex   sync.Mutex
	active  *pluginActivation
}

// pluginActivation owns the reversible resources of one Plugin activation.
type pluginActivation struct {
	gateway     *pluginGateway
	effectCalls sync.WaitGroup
	driverDone  chan struct{}
}

func newPluginActivation() *pluginActivation {
	return &pluginActivation{
		gateway:    newPluginGateway(),
		driverDone: make(chan struct{}),
	}
}

func (activation *pluginActivation) eventGateway() agentEvents {
	return activation.gateway
}

func (activation *pluginActivation) waterfallGateway() agentWaterfalls {
	return activation.gateway
}

func (activation *pluginActivation) start(owner *Plugin) {
	go activation.drive(owner)
}

func (activation *pluginActivation) drive(owner *Plugin) {
	defer close(activation.driverDone)
	for {
		select {
		case request := <-activation.gateway.requests:
			activation.effectCalls.Add(1)
			go func() {
				defer activation.effectCalls.Done()
				owner.executeEffect(request)
			}()
		case <-activation.gateway.stopped:
			return
		}
	}
}

func (activation *pluginActivation) close() {
	activation.gateway.stop(errors.New("agentloop: Plugin is stopping"))
	<-activation.driverDone
	activation.effectCalls.Wait()
}

// NewPlugin constructs the one AgentLoop module Plugin.
func NewPlugin(loopSettings Settings, options RuntimeOptions) (*Plugin, error) {
	validated, err := validateSettings(loopSettings)
	if err != nil {
		return nil, err
	}
	owner := &Plugin{}
	owner.factory = newFactory(
		validated.MaxParallelToolCalls,
		options.ObserverError,
	)
	return owner, nil
}

func (owner *Plugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: PluginName,
		Provides: []plugin.ProvidedService{
			plugin.NewProvidedService[agent.Factory](owner.factory),
		},
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[session.LiveStore](),
			plugin.ServiceOf[llm.LlmRuntime](),
			plugin.ServiceOf[systemprompt.PromptLayerFactory](),
			plugin.ServiceOf[tools.ToolLayerFactory](),
		},
		Optional: []plugin.ServiceType{
			plugin.ServiceOf[sesspersist.Persistence](),
		},
	}
}

func (owner *Plugin) Apply(requestContext context.Context) error {
	if requestContext == nil {
		return errors.New("agentloop: Plugin Apply Context is nil")
	}
	if err := requestContext.Err(); err != nil {
		return err
	}
	sessions, err := plugin.Require[session.LiveStore](owner)
	if err != nil {
		return err
	}
	models, err := plugin.Require[llm.LlmRuntime](owner)
	if err != nil {
		return err
	}
	prompts, err := plugin.Require[systemprompt.PromptLayerFactory](owner)
	if err != nil {
		return err
	}
	toolLayers, err := plugin.Require[tools.ToolLayerFactory](owner)
	if err != nil {
		return err
	}
	persistence, _ := plugin.Resolve[sesspersist.Persistence](owner)
	activation := newPluginActivation()
	owner.mutex.Lock()
	if owner.active != nil {
		owner.mutex.Unlock()
		return errors.New("agentloop: Plugin is already active")
	}
	owner.active = activation
	owner.mutex.Unlock()
	if err = owner.factory.enterRuntime(factoryDependencies{
		sessions:    sessions,
		persistence: persistence,
		models:      models,
		prompts:     prompts,
		toolLayers:  toolLayers,
		events:      activation.eventGateway(),
		waterfalls:  activation.waterfallGateway(),
	}); err != nil {
		owner.mutex.Lock()
		owner.active = nil
		owner.mutex.Unlock()
		return err
	}
	activation.start(owner)
	return requestContext.Err()
}

func (owner *Plugin) Dispose(closeContext context.Context) error {
	if owner == nil {
		return nil
	}
	if closeContext == nil {
		closeContext = context.Background()
	}
	owner.mutex.Lock()
	activation := owner.active
	owner.active = nil
	owner.mutex.Unlock()
	if activation == nil {
		return nil
	}
	owner.factory.leaveRuntime()
	activation.close()
	select {
	case <-closeContext.Done():
		return context.Cause(closeContext)
	default:
		return nil
	}
}

func (owner *Plugin) executeEffect(call effectCall) {
	switch requested := call.(type) {
	case eventCall:
		fact, matches := requested.fact.(plugin.Event)
		if !matches {
			requested.result <- errors.New(
				"agentloop: Agent AgentEvent has no Plugin event metadata",
			)
			return
		}
		requested.result <- plugin.PublishEvent(
			requested.requestContext,
			owner,
			fact,
		)
	case preStepCall:
		decision, err := plugin.Run(
			requested.requestContext,
			owner,
			requested.notice,
			requested.terminal,
		)
		requested.result <- preStepCallResult{
			decision: decision,
			err:      err,
		}
	case requestResolutionCall:
		resolution, err := plugin.Run(
			requested.requestContext,
			owner,
			requested.notice,
			requested.terminal,
		)
		requested.result <- requestResolutionCallResult{
			resolution: resolution,
			err:        err,
		}
	case requestErrorCall:
		action, err := plugin.Run(
			requested.requestContext,
			owner,
			requested.notice,
			requested.terminal,
		)
		requested.result <- requestErrorCallResult{
			action: action,
			err:    err,
		}
	}
}

var _ plugin.Plugin = (*Plugin)(nil)
