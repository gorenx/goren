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

// scopeID identifies one exact Agent Scope mount within a ScopeSet. It is
// process-local and never substitutes for Session or Agent identity.
type scopeID uint64

// ScopeSet owns the exact Plugin Handle for every private Agent Scope
// mounted below it. AgentScope requests teardown here and never removes
// itself from Plugin topology.
type ScopeSet struct {
	plugin.Base
	mutex  sync.Mutex
	nextID scopeID
	// handles maps a process-local scopeID to the exact mounted Plugin handle.
	// Presence means that Scope is still structurally mounted below ScopeSet;
	// the value authorizes unloading only that exact mount.
	handles map[scopeID]plugin.Handle
}

func newScopeSet() *ScopeSet {
	return &ScopeSet{
		handles: make(map[scopeID]plugin.Handle),
	}
}

func (*ScopeSet) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: PluginName + "/scope-set",
	}
}

func (*ScopeSet) Apply(requestContext context.Context) error {
	return requestContext.Err()
}

func (scopes *ScopeSet) Dispose(context.Context) error {
	if scopes == nil {
		return nil
	}
	scopes.mutex.Lock()
	deferred := len(scopes.handles)
	scopes.handles = make(map[scopeID]plugin.Handle)
	scopes.mutex.Unlock()
	if deferred != 0 {
		return errors.New("agentloop: ScopeSet stopped before its Agent Scopes")
	}
	return nil
}

func (scopes *ScopeSet) Prepare(
	agentOptions agent.Options,
	headerSnapshot session.Header,
) scopePreparation {
	scopes.mutex.Lock()
	scopes.nextID++
	identifier := scopes.nextID
	scopes.mutex.Unlock()
	root := newAgentScope(agentOptions, headerSnapshot, identifier, scopes)
	return &agentScopePreparation{
		scopes:  scopes,
		root:    root,
		scopeID: identifier,
	}
}

func (scopes *ScopeSet) mount(
	requestContext context.Context,
	identifier scopeID,
	root *AgentScope,
) (scopedplugin.Scope, error) {
	if scopes == nil || identifier == 0 || root == nil {
		return nil, errors.New("agentloop: Agent Scopes are unavailable")
	}
	handle, err := plugin.MountScopedChild(
		requestContext,
		scopes,
		root,
	)
	if err != nil {
		return nil, errors.Join(
			err,
			root.Dispose(context.WithoutCancel(requestContext)),
		)
	}
	scopes.mutex.Lock()
	scopes.handles[identifier] = handle
	scopes.mutex.Unlock()
	return root, nil
}

func (scopes *ScopeSet) release(
	closeContext context.Context,
	identifier scopeID,
) (bool, error) {
	if scopes == nil || identifier == 0 {
		return false, nil
	}
	scopes.mutex.Lock()
	handle, mounted := scopes.handles[identifier]
	scopes.mutex.Unlock()
	if !mounted || handle.ID() == 0 {
		return false, nil
	}
	closeErr := plugin.UnloadChild(
		context.WithoutCancel(closeContext),
		scopes,
		handle,
	)
	if errors.Is(closeErr, plugin.ErrPluginNotActive) ||
		errors.Is(closeErr, plugin.ErrPluginNotBound) {
		return true, nil
	}
	return true, closeErr
}

func (scopes *ScopeSet) forget(identifier scopeID) {
	if scopes == nil || identifier == 0 {
		return
	}
	scopes.mutex.Lock()
	delete(scopes.handles, identifier)
	scopes.mutex.Unlock()
}

var _ agentScopeFactory = (*ScopeSet)(nil)
var _ plugin.Plugin = (*ScopeSet)(nil)
