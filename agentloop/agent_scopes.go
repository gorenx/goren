package agentloop

import (
	"context"
	"errors"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agent/scopedplugin"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
)

// agentScopes owns the exact Plugin Handle for every private Agent Scope
// mounted below the Agent Loop Plugin. Scope roots request teardown here and
// never remove themselves from Plugin topology.
type agentScopes struct {
	mutex   sync.Mutex
	owner   *Plugin
	handles map[*agentScopeRoot]plugin.Handle
}

func newAgentScopes(owner *Plugin) *agentScopes {
	return &agentScopes{
		owner:   owner,
		handles: make(map[*agentScopeRoot]plugin.Handle),
	}
}

func (scopes *agentScopes) Prepare(
	loopOptions agent.Options,
	headerSnapshot session.Header,
) scopePreparation {
	root := newAgentScopeRoot(loopOptions, headerSnapshot, scopes)
	return &agentScopePreparation{
		scopes: scopes,
		root:   root,
	}
}

func (scopes *agentScopes) mount(
	requestContext context.Context,
	root *agentScopeRoot,
) (scopedplugin.Scope, error) {
	if scopes == nil || scopes.owner == nil || root == nil {
		return nil, errors.New("agentloop: Agent Scopes are unavailable")
	}
	handle, err := plugin.MountScopedChild(
		requestContext,
		scopes.owner,
		root,
	)
	if err != nil {
		return nil, errors.Join(
			err,
			root.Dispose(context.WithoutCancel(requestContext)),
		)
	}
	scopes.mutex.Lock()
	scopes.handles[root] = handle
	scopes.mutex.Unlock()
	return root, nil
}

func (scopes *agentScopes) release(
	closeContext context.Context,
	root *agentScopeRoot,
) error {
	if scopes == nil || root == nil {
		return nil
	}
	scopes.mutex.Lock()
	handle, mounted := scopes.handles[root]
	scopes.mutex.Unlock()
	if !mounted || handle.ID() == 0 {
		return root.Dispose(context.WithoutCancel(closeContext))
	}
	closeErr := plugin.UnloadChild(
		context.WithoutCancel(closeContext),
		scopes.owner,
		handle,
	)
	if errors.Is(closeErr, plugin.ErrPluginNotActive) ||
		errors.Is(closeErr, plugin.ErrPluginNotBound) {
		return nil
	}
	return closeErr
}

func (scopes *agentScopes) forget(root *agentScopeRoot) {
	if scopes == nil || root == nil {
		return
	}
	scopes.mutex.Lock()
	delete(scopes.handles, root)
	scopes.mutex.Unlock()
}

var _ agentScopeFactory = (*agentScopes)(nil)
