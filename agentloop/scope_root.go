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

// scopeHost is the Agent Loop consumer port for preparing one private runtime
// Scope. The Plugin adapter is the only implementation.
type scopeHost interface {
	Prepare(agent.Options, session.Header) scopePreparation
}

// scopePreparation breaks the construction cycle deliberately: the business
// Agent receives its Scope runtime port before the Plugin adapter is mounted, then
// Mount binds that exact Agent and activates its private Scope atomically.
type scopePreparation interface {
	Runtime() agent.AgentScopeRuntime
	BindTeardown(agent.AgentTeardown) error
	Mount(context.Context, *ReactLoopAgent) (scopedplugin.Scope, error)
	Rollback(context.Context) error
}

type pluginScopeHost struct {
	owner *Plugin
}

func (host pluginScopeHost) Prepare(
	loopOptions agent.Options,
	headerSnapshot session.Header,
) scopePreparation {
	root := newAgentScopeRoot(loopOptions, headerSnapshot)
	return &pluginScopePreparation{
		host: host,
		root: root,
	}
}

type pluginScopePreparation struct {
	host pluginScopeHost
	root *agentScopeRoot
}

func (preparation *pluginScopePreparation) Runtime() agent.AgentScopeRuntime {
	return preparation.root
}

func (preparation *pluginScopePreparation) BindTeardown(
	teardownTarget agent.AgentTeardown,
) error {
	return preparation.root.bindTeardown(teardownTarget)
}

func (preparation *pluginScopePreparation) Mount(
	requestContext context.Context,
	subject *ReactLoopAgent,
) (scopedplugin.Scope, error) {
	if preparation == nil || preparation.root == nil || preparation.host.owner == nil {
		return nil, errors.New("agentloop: Agent Scope preparation is unavailable")
	}
	if err := preparation.root.bind(subject); err != nil {
		return nil, err
	}
	handle, err := plugin.MountScopedChild(
		requestContext,
		preparation.host.owner,
		preparation.root,
	)
	if err != nil {
		return nil, errors.Join(
			err,
			preparation.root.Dispose(context.WithoutCancel(requestContext)),
		)
	}
	preparation.root.recordMount(preparation.host.owner, handle)
	return preparation.root, nil
}

func (preparation *pluginScopePreparation) Rollback(
	closeContext context.Context,
) error {
	if preparation == nil || preparation.root == nil {
		return nil
	}
	return preparation.root.Teardown(closeContext)
}

// agentScopeRoot is the Plugin adapter for one exact Agent. It owns scoped
// runtime bindings and translates Agent-owned runtime contracts to Plugin
// Events and Waterfalls; it owns no Agent lifecycle state.
type agentScopeRoot struct {
	plugin.Base
	children []plugin.ChildPlugin

	mutex        sync.Mutex
	subject      *ReactLoopAgent
	mountOwner   plugin.Plugin
	mountHandle  plugin.Handle
	mounted      bool
	resources    []agent.ScopeResource
	disposeOnce  sync.Once
	disposeDone  chan struct{}
	disposeErr   error
	teardownOnce sync.Once
	teardownErr  error
	teardown     agent.AgentTeardown
}

func newAgentScopeRoot(
	loopOptions agent.Options,
	headerSnapshot session.Header,
) *agentScopeRoot {
	return &agentScopeRoot{
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
				Instance:  newAgentVariables(loopOptions, headerSnapshot),
				Placement: plugin.SameScope,
				Phase:     plugin.ActivationMain,
			},
		},
		disposeDone: make(chan struct{}),
	}
}

func (root *agentScopeRoot) bindTeardown(
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

func (root *agentScopeRoot) bind(subject *ReactLoopAgent) error {
	if root == nil || subject == nil {
		return errors.New("agentloop: Agent Scope requires an Agent")
	}
	root.mutex.Lock()
	defer root.mutex.Unlock()
	if root.subject != nil {
		return errors.New("agentloop: Agent Scope is already bound")
	}
	root.subject = subject
	root.children = append(
		root.children,
		plugin.ChildPlugin{
			Instance: &agentRuntimeAdapter{
				subject: subject,
			},
			Placement: plugin.SameScope,
			Phase:     plugin.ActivationMain,
		},
	)
	return nil
}

func (root *agentScopeRoot) recordMount(
	owner plugin.Plugin,
	handle plugin.Handle,
) {
	root.mutex.Lock()
	root.mountOwner = owner
	root.mountHandle = handle
	root.mounted = true
	root.mutex.Unlock()
}

func (root *agentScopeRoot) Manifest() plugin.Manifest {
	root.mutex.Lock()
	children := append([]plugin.ChildPlugin(nil), root.children...)
	root.mutex.Unlock()
	return plugin.Manifest{
		Name:     PluginName + "/agent-scope",
		Children: children,
	}
}

func (root *agentScopeRoot) Apply(requestContext context.Context) error {
	return requestContext.Err()
}

func (root *agentScopeRoot) Dispose(closeContext context.Context) error {
	if closeContext == nil {
		closeContext = context.Background()
	}
	root.disposeOnce.Do(func() {
		root.mutex.Lock()
		resources := append([]agent.ScopeResource(nil), root.resources...)
		root.resources = nil
		subject := root.subject
		root.mutex.Unlock()

		for index := len(resources) - 1; index >= 0; index-- {
			root.disposeErr = errors.Join(
				root.disposeErr,
				resources[index].Dispose(closeContext),
			)
		}
		if subject != nil {
			root.disposeErr = errors.Join(
				root.disposeErr,
				subject.shutdown(closeContext),
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

func (root *agentScopeRoot) Agent() agent.Agent {
	root.mutex.Lock()
	subject := root.subject
	root.mutex.Unlock()
	return subject
}

func (root *agentScopeRoot) MountPlugin(
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

func (root *agentScopeRoot) Own(resource agent.ScopeResource) error {
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

func (root *agentScopeRoot) Dispatch(
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

func (root *agentScopeRoot) ResolvePreStep(
	requestContext context.Context,
	notice agent.PreStepNotice,
	terminal agent.PreStepAction,
) (agent.PreStepDecision, error) {
	return plugin.Run(requestContext, root, notice, terminal)
}

func (root *agentScopeRoot) ResolveRequest(
	requestContext context.Context,
	notice agent.RequestNotice,
	terminal agent.RequestAction,
) (agent.RequestResolution, error) {
	return plugin.Run(requestContext, root, notice, terminal)
}

func (root *agentScopeRoot) ResolveRequestError(
	requestContext context.Context,
	notice agent.RequestErrorNotice,
	terminal agent.RequestErrorHandler,
) (agent.RequestErrorAction, error) {
	return plugin.Run(requestContext, root, notice, terminal)
}

func (root *agentScopeRoot) Provision(
	requestContext context.Context,
	provisioner agent.Provisioner,
) error {
	if provisioner == nil {
		return errors.New("agentloop: Agent Provisioner is nil")
	}
	return agent.ApplyProvisioning(requestContext, root, provisioner)
}

func (root *agentScopeRoot) Teardown(closeContext context.Context) error {
	if root == nil {
		return nil
	}
	if closeContext == nil {
		closeContext = context.Background()
	}
	root.teardownOnce.Do(func() {
		root.mutex.Lock()
		mounted := root.mounted
		owner := root.mountOwner
		handle := root.mountHandle
		root.mutex.Unlock()
		if !mounted || owner == nil || handle.ID() == 0 {
			root.teardownErr = root.Dispose(closeContext)
			return
		}
		root.teardownErr = plugin.UnloadChild(
			context.WithoutCancel(closeContext),
			owner,
			handle,
		)
		if errors.Is(root.teardownErr, plugin.ErrPluginNotActive) ||
			errors.Is(root.teardownErr, plugin.ErrPluginNotBound) {
			root.teardownErr = nil
		}
	})
	return root.teardownErr
}

type pluginEffect struct {
	mutex  sync.Mutex
	parent *agentScopeRoot
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

var _ agent.Scope = (*agentScopeRoot)(nil)
var _ scopedplugin.Scope = (*agentScopeRoot)(nil)
var _ agent.AgentScopeRuntime = (*agentScopeRoot)(nil)
var _ agent.ScopeResource = (*pluginEffect)(nil)
