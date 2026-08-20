package agentloop

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/tools"
)

// agentTree is one private ownership root. It creates the Agent Scope and
// orders scoped overlays before ReactLoopAgent captures its dependencies.
type agentTree struct {
	plugin.Base
	children []plugin.ChildPlugin
}

func newAgentTree(
	subject *ReactLoopAgent,
	loopOptions agent.Options,
	extensions []plugin.Plugin,
	membership *agentMembership,
) *agentTree {
	children := []plugin.ChildPlugin{
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
	}
	for _, extension := range extensions {
		children = append(
			children,
			plugin.ChildPlugin{
				Instance:  extension,
				Placement: plugin.SameScope,
				Phase:     plugin.ActivationMain,
			},
		)
	}
	children = append(
		children,
		plugin.ChildPlugin{
			Instance:  membership,
			Placement: plugin.SameScope,
			Phase:     plugin.ActivationCommit,
		},
	)
	return &agentTree{
		children: children,
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

func (*agentTree) Dispose(context.Context) error {
	return nil
}

// mountAgentTree assembles one complete Agent tree before handing the detached
// declaration to Runtime. Runtime remains responsible for validation,
// activation ordering, rollback, and eventual reverse teardown.
func (owner *Plugin) mountAgentTree(
	requestContext context.Context,
	conversation *session.Session,
	loopOptions agent.Options,
	extensions []plugin.Plugin,
	startSource agent.SessionStartSource,
) (agent.Handle, error) {
	if conversation == nil {
		return agent.Handle{}, errors.New("agentloop: prepared Session is nil")
	}
	subject, err := newReactLoopAgent(
		conversation,
		loopOptions,
		owner.settings.MaxParallelToolCalls,
		owner.failures,
	)
	if err != nil {
		return agent.Handle{}, err
	}
	lifecycle := newAgentLifecycle(owner)
	subject.lifecycle = lifecycle
	initiator, _ := agent.InitiatorFrom(requestContext)
	membership := newAgentMembership(
		owner.lifecycles,
		owner.runtimeContextEvents,
		owner.failures,
		lifecycle,
		subject,
		startSource,
		initiator,
	)
	tree := newAgentTree(
		subject,
		loopOptions,
		extensions,
		membership,
	)
	rootHandle, err := plugin.MountScopedChild(
		requestContext,
		owner,
		tree,
	)
	if err != nil {
		return agent.Handle{}, err
	}
	if !lifecycle.attachRoot(rootHandle) {
		return agent.Handle{}, errors.Join(
			errors.New(
				"agentloop: Agent tree stopped before Handle attachment",
			),
			lifecycle.Dispose(requestContext),
		)
	}
	return agent.NewHandle(subject, lifecycle)
}
