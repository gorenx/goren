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
	gateway *pluginGateway

	effectCalls sync.WaitGroup
	driverDone  chan struct{}
	disposeOnce sync.Once
	disposeDone chan struct{}
	disposeErr  error
}

// NewPlugin constructs the one AgentLoop module Plugin.
func NewPlugin(loopSettings Settings, options RuntimeOptions) (*Plugin, error) {
	validated, err := validateSettings(loopSettings)
	if err != nil {
		return nil, err
	}
	gateway := newPluginGateway()
	owner := &Plugin{
		gateway:     gateway,
		driverDone:  make(chan struct{}),
		disposeDone: make(chan struct{}),
	}
	owner.factory = newFactory(
		validated.MaxParallelToolCalls,
		options.ObserverError,
		gateway,
		gateway,
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
	if err = owner.factory.enterRuntime(factoryDependencies{
		sessions:    sessions,
		persistence: persistence,
		models:      models,
		prompts:     prompts,
		toolLayers:  toolLayers,
	}); err != nil {
		return err
	}
	go owner.driveEffects()
	return requestContext.Err()
}

func (owner *Plugin) Dispose(closeContext context.Context) error {
	if owner == nil {
		return nil
	}
	if closeContext == nil {
		closeContext = context.Background()
	}
	owner.disposeOnce.Do(func() {
		owner.factory.leaveRuntime()
		owner.gateway.stop(errors.New("agentloop: Plugin is stopping"))
		<-owner.driverDone
		owner.effectCalls.Wait()
		close(owner.disposeDone)
	})
	select {
	case <-owner.disposeDone:
		return owner.disposeErr
	case <-closeContext.Done():
		return context.Cause(closeContext)
	}
}

func (owner *Plugin) driveEffects() {
	defer close(owner.driverDone)
	for {
		select {
		case request := <-owner.gateway.requests:
			owner.effectCalls.Add(1)
			go func() {
				defer owner.effectCalls.Done()
				owner.executeEffect(request)
			}()
		case <-owner.gateway.stopped:
			return
		}
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
