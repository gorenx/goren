package agentloop

import (
	"context"
	"errors"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agent/scopedplugin"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/tools"
)

// AgentScope is the Plugin adapter for one exact Agent. It owns scoped
// runtime bindings and translates Agent-owned runtime contracts to Plugin
// Events and Waterfalls; it owns no Agent lifecycle state.
type scopeParent interface {
	release(context.Context, scopeID) (bool, error)
	forget(scopeID)
}

type AgentScope struct {
	plugin.Base
	children []plugin.ChildPlugin

	mutex        sync.Mutex
	identifier   scopeID
	parent       scopeParent
	provider     *AgentProvider
	resources    []agent.ScopeResource
	disposeOnce  sync.Once
	disposeDone  chan struct{}
	disposeErr   error
	teardownOnce sync.Once
	teardownErr  error
	teardown     agent.AgentTeardown
}

func newAgentScope(
	agentOptions agent.Options,
	headerSnapshot session.Header,
	identifier scopeID,
	parent scopeParent,
) *AgentScope {
	return &AgentScope{
		identifier: identifier,
		parent:     parent,
		children: []plugin.ChildPlugin{
			{
				Instance:  systemprompt.NewOverlay(systemprompt.RegistryOptions{}),
				Placement: plugin.SameScope,
				Phase:     plugin.ActivationMain,
			},
			{
				Instance:  tools.NewOverlay(),
				Placement: plugin.SameScope,
				Phase:     plugin.ActivationMain,
			},
			{
				Instance:  newAgentVariables(agentOptions, headerSnapshot),
				Placement: plugin.SameScope,
				Phase:     plugin.ActivationMain,
			},
		},
		disposeDone: make(chan struct{}),
	}
}

func (root *AgentScope) bindTeardown(
	teardownTarget agent.AgentTeardown,
) error {
	if root == nil || teardownTarget == nil {
		return errors.New("agentloop: Agent teardown is unavailable")
	}
	root.mutex.Lock()
	defer root.mutex.Unlock()
	if root.teardown != nil {
		return errors.New("agentloop: Agent teardown is already bound")
	}
	root.teardown = teardownTarget
	return nil
}

func (root *AgentScope) bind(subject *ReactLoopAgent) error {
	if root == nil || subject == nil {
		return errors.New("agentloop: Agent Scope requires an Agent")
	}
	root.mutex.Lock()
	defer root.mutex.Unlock()
	if root.provider != nil {
		return errors.New("agentloop: Agent Scope is already bound")
	}
	root.provider = &AgentProvider{
		subject: subject,
	}
	root.children = append(
		root.children,
		plugin.ChildPlugin{
			Instance:  root.provider,
			Placement: plugin.SameScope,
			Phase:     plugin.ActivationMain,
		},
	)
	return nil
}

func (root *AgentScope) Manifest() plugin.Manifest {
	root.mutex.Lock()
	children := append([]plugin.ChildPlugin(nil), root.children...)
	root.mutex.Unlock()
	return plugin.Manifest{
		Name:     PluginName + "/agent-scope",
		Children: children,
	}
}

func (root *AgentScope) Apply(requestContext context.Context) error {
	return requestContext.Err()
}

func (root *AgentScope) Dispose(closeContext context.Context) error {
	if closeContext == nil {
		closeContext = context.Background()
	}
	root.disposeOnce.Do(func() {
		if root.parent != nil {
			root.parent.forget(root.identifier)
		}
		root.mutex.Lock()
		resources := append([]agent.ScopeResource(nil), root.resources...)
		root.resources = nil
		root.mutex.Unlock()

		for index := len(resources) - 1; index >= 0; index-- {
			root.disposeErr = errors.Join(
				root.disposeErr,
				resources[index].Dispose(closeContext),
			)
		}
		if root.teardown != nil {
			root.teardown.FinishTeardown(root.disposeErr)
		}
		close(root.disposeDone)
	})
	select {
	case <-root.disposeDone:
		return root.disposeErr
	case <-closeContext.Done():
		return context.Cause(closeContext)
	}
}

func (root *AgentScope) Agent() agent.Agent {
	root.mutex.Lock()
	provider := root.provider
	root.mutex.Unlock()
	if provider == nil {
		return nil
	}
	return provider.Agent()
}

func (root *AgentScope) MountPlugin(
	requestContext context.Context,
	instance plugin.Plugin,
) (agent.ScopeResource, error) {
	if root == nil || instance == nil {
		return nil, errors.New("agentloop: Agent Scope requires a Plugin")
	}
	handle, err := plugin.MountChild(requestContext, root, instance)
	if err != nil {
		return nil, err
	}
	return &pluginEffect{
		parent: root,
		handle: handle,
	}, nil
}

func (root *AgentScope) Own(resource agent.ScopeResource) error {
	if root == nil || resource == nil {
		return errors.New("agentloop: Agent Scope Resource is nil")
	}
	root.mutex.Lock()
	defer root.mutex.Unlock()
	select {
	case <-root.disposeDone:
		return errors.New("agentloop: Agent Scope is closing")
	default:
	}
	root.resources = append(root.resources, resource)
	return nil
}

func (root *AgentScope) Dispatch(
	requestContext context.Context,
	fact agent.RuntimeEvent,
) error {
	runtimeFact, matches := fact.(plugin.Event)
	if !matches {
		return errors.New(
			"agentloop: Agent RuntimeEvent has no Plugin event metadata",
		)
	}
	return plugin.PublishEvent(requestContext, root, runtimeFact)
}

func (root *AgentScope) ResolvePreStep(
	requestContext context.Context,
	notice agent.PreStepNotice,
	terminal agent.PreStepAction,
) (agent.PreStepDecision, error) {
	return plugin.Run(requestContext, root, notice, terminal)
}

func (root *AgentScope) ResolveRequest(
	requestContext context.Context,
	notice agent.RequestNotice,
	terminal agent.RequestAction,
) (agent.RequestResolution, error) {
	return plugin.Run(requestContext, root, notice, terminal)
}

func (root *AgentScope) ResolveRequestError(
	requestContext context.Context,
	notice agent.RequestErrorNotice,
	terminal agent.RequestErrorHandler,
) (agent.RequestErrorAction, error) {
	return plugin.Run(requestContext, root, notice, terminal)
}

func (root *AgentScope) Provision(
	requestContext context.Context,
	provisioner agent.Provisioner,
) error {
	if provisioner == nil {
		return errors.New("agentloop: Agent Provisioner is nil")
	}
	return agent.ApplyProvisioning(requestContext, root, provisioner)
}

func (root *AgentScope) Teardown(closeContext context.Context) error {
	if root == nil {
		return nil
	}
	if closeContext == nil {
		closeContext = context.Background()
	}
	root.teardownOnce.Do(func() {
		if root.parent == nil {
			root.teardownErr = root.Dispose(closeContext)
			return
		}
		mounted, releaseErr := root.parent.release(
			closeContext,
			root.identifier,
		)
		root.teardownErr = releaseErr
		if !mounted {
			root.teardownErr = errors.Join(
				root.teardownErr,
				root.Dispose(closeContext),
			)
		}
	})
	return root.teardownErr
}

type pluginEffect struct {
	mutex  sync.Mutex
	parent *AgentScope
	handle plugin.Handle
	closed bool
}

func (effect *pluginEffect) Dispose(closeContext context.Context) error {
	if effect == nil {
		return nil
	}
	effect.mutex.Lock()
	if effect.closed {
		effect.mutex.Unlock()
		return nil
	}
	effect.closed = true
	parent := effect.parent
	handle := effect.handle
	effect.mutex.Unlock()
	if parent == nil || handle.ID() == 0 {
		return nil
	}
	if closeContext == nil {
		closeContext = context.Background()
	}
	err := plugin.UnloadChild(
		context.WithoutCancel(closeContext),
		parent,
		handle,
	)
	if errors.Is(err, plugin.ErrPluginNotActive) ||
		errors.Is(err, plugin.ErrPluginNotBound) {
		return nil
	}
	return err
}

var _ agent.Scope = (*AgentScope)(nil)
var _ scopedplugin.Scope = (*AgentScope)(nil)
var _ agent.AgentScopeRuntime = (*AgentScope)(nil)
var _ agent.ScopeResource = (*pluginEffect)(nil)
