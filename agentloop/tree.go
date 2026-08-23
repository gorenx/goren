package agentloop

import (
	"context"
	"errors"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/tools"
)

// agentTree is one Agent's private runtime Scope root. It owns the base runtime
// Plugins and every effect acquired while composing the unpublished Agent.
// Membership publication remains a separate creation-transaction stage.
type agentTree struct {
	plugin.Base
	subject  agent.Agent
	children []plugin.ChildPlugin

	mutex   sync.Mutex
	effects []agent.Effect
	closing bool
	closed  bool
}

func newAgentTree(
	subject *ReactLoopAgent,
	loopOptions agent.Options,
) *agentTree {
	return &agentTree{
		subject: subject,
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
				Instance: newAgentVariables(
					loopOptions,
					subject.conversation.Header(),
				),
				Placement: plugin.SameScope,
				Phase:     plugin.ActivationMain,
			},
			{
				Instance:  subject,
				Placement: plugin.SameScope,
				Phase:     plugin.ActivationMain,
			},
		},
	}
}

func (tree *agentTree) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name:     PluginName + "/agent-tree",
		Children: append([]plugin.ChildPlugin(nil), tree.children...),
	}
}

func (*agentTree) Apply(requestContext context.Context) error {
	return requestContext.Err()
}

func (tree *agentTree) AgentValue() agent.Agent {
	return tree.subject
}

func (tree *agentTree) Mount(
	requestContext context.Context,
	instance plugin.Plugin,
) (agent.Effect, error) {
	if tree == nil || instance == nil {
		return nil, errors.New("agentloop: Agent Scope requires a Plugin")
	}
	handle, err := plugin.MountChild(requestContext, tree, instance)
	if err != nil {
		return nil, err
	}
	return &pluginEffect{
		parent: tree,
		handle: handle,
	}, nil
}

func (tree *agentTree) Own(effect agent.Effect) error {
	if tree == nil {
		return errors.New("agentloop: Agent Scope is unavailable")
	}
	if effect == nil {
		return errors.New("agentloop: Agent Scope Effect is nil")
	}
	tree.mutex.Lock()
	defer tree.mutex.Unlock()
	if tree.closing || tree.closed {
		return errors.New("agentloop: Agent Scope is closing")
	}
	tree.effects = append(tree.effects, effect)
	return nil
}

func (tree *agentTree) Dispose(closeContext context.Context) error {
	if closeContext == nil {
		closeContext = context.Background()
	}
	tree.mutex.Lock()
	if tree.closed {
		tree.mutex.Unlock()
		return nil
	}
	if tree.closing {
		tree.mutex.Unlock()
		return nil
	}
	tree.closing = true
	effects := append([]agent.Effect(nil), tree.effects...)
	tree.mutex.Unlock()

	var closeErr error
	for index := len(effects) - 1; index >= 0; index-- {
		closeErr = errors.Join(closeErr, effects[index].Dispose(closeContext))
	}
	tree.mutex.Lock()
	tree.effects = nil
	tree.closed = true
	tree.closing = false
	tree.mutex.Unlock()
	return closeErr
}

type pluginEffect struct {
	mutex  sync.Mutex
	parent *agentTree
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

var _ agent.Scope = (*agentTree)(nil)
var _ agent.Effect = (*pluginEffect)(nil)
