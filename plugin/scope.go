package plugin

import (
	"context"
	"errors"
	"strings"
	"sync"
)

var closedLifetime = func() context.Context {
	closedContext, cancelLifetime := context.WithCancelCause(context.Background())
	cancelLifetime(ErrContextClosed)
	return closedContext
}()

// Context is the restricted Runtime handle of one Fiber at one Scope. It is
// intentionally not a context.Context and exposes neither configuration nor a
// string-based service locator.
type Context struct {
	runtime     *Runtime
	ownerFiber  *fiber
	scope       *Scope
	lifetime    context.Context
	transaction *mountTransaction
}

func newPluginContext(
	runtimeEngine *Runtime,
	ownerFiber *fiber,
	ownerScope *Scope,
	fiberLifetime context.Context,
	transaction *mountTransaction,
) *Context {
	return &Context{
		runtime:     runtimeEngine,
		ownerFiber:  ownerFiber,
		scope:       ownerScope,
		lifetime:    fiberLifetime,
		transaction: transaction,
	}
}

// Lifetime returns a Context cancelled when this Fiber begins stopping.
func (pluginContext *Context) Lifetime() context.Context {
	if pluginContext == nil || pluginContext.lifetime == nil {
		return closedLifetime
	}
	return pluginContext.lifetime
}

// FiberID returns the identity of the current activation attempt.
func (pluginContext *Context) FiberID() FiberID {
	if pluginContext == nil || pluginContext.ownerFiber == nil {
		return 0
	}
	return pluginContext.ownerFiber.id
}

// Scope returns the current visibility and routing Scope.
func (pluginContext *Context) Scope() *Scope {
	if pluginContext == nil {
		return nil
	}
	return pluginContext.scope
}

func (pluginContext *Context) register(entry runtimeEntry) error {
	if pluginContext == nil || pluginContext.runtime == nil || pluginContext.ownerFiber == nil {
		return ErrContextClosed
	}
	if pluginContext.scope == nil || pluginContext.scope.isClosed() {
		return ErrContextClosed
	}
	if pluginContext.transaction != nil && pluginContext.transaction.state == mountOpen {
		return pluginContext.transaction.stageEntry(pluginContext.scope, entry)
	}
	return ErrRegistrationClosed
}

// ChildScope creates a visibility branch with the same Fiber lifetime.
func (pluginContext *Context) ChildScope(scopeLabel string) (*Context, error) {
	if pluginContext == nil || pluginContext.runtime == nil ||
		pluginContext.ownerFiber == nil || pluginContext.scope == nil {
		return nil, ErrContextClosed
	}
	trimmedLabel := strings.TrimSpace(scopeLabel)
	if trimmedLabel == "" || trimmedLabel != scopeLabel {
		return nil, errors.New("plugin: Scope label must be non-empty and trimmed")
	}
	parentScope := pluginContext.scope
	parentScope.mutex.Lock()
	defer parentScope.mutex.Unlock()
	if parentScope.closed {
		return nil, ErrContextClosed
	}
	if _, exists := parentScope.children[scopeLabel]; exists {
		return nil, errors.New("plugin: duplicate child Scope label")
	}
	createdScope := &Scope{
		runtime:    pluginContext.runtime,
		ownerFiber: pluginContext.ownerFiber,
		parent:     parentScope,
		target: ScopeKey{
			token: &scopeToken{
				parent: parentScope.target.token,
				depth:  parentScope.depth() + 1,
			},
		},
		label:    scopeLabel,
		children: make(map[string]*Scope),
	}
	parentScope.children[scopeLabel] = createdScope
	return newPluginContext(
		pluginContext.runtime,
		pluginContext.ownerFiber,
		createdScope,
		pluginContext.lifetime,
		pluginContext.transaction,
	), nil
}

// Mount attaches instance at the current Scope. The mounted Plugin receives
// its own Fiber, and the current Fiber owns that entire child lifecycle.
func (pluginContext *Context) Mount(
	mountContext context.Context,
	instance Plugin,
) (Handle, error) {
	if pluginContext == nil || pluginContext.runtime == nil || pluginContext.ownerFiber == nil {
		return Handle{}, ErrContextClosed
	}
	if pluginContext.scope == nil || pluginContext.scope.isClosed() {
		if pluginContext.transaction != nil && pluginContext.transaction.state == mountOpen {
			return Handle{}, pluginContext.transaction.recordFailure(ErrContextClosed)
		}
		return Handle{}, ErrContextClosed
	}
	if pluginContext.transaction != nil && pluginContext.transaction.state == mountOpen {
		return pluginContext.runtime.supervisor.mountDuringApply(
			mountContext,
			pluginContext,
			instance,
		)
	}
	pluginContext.runtime.state.RLock()
	active := pluginContext.ownerFiber.state == FiberActive
	pluginContext.runtime.state.RUnlock()
	if !active {
		return Handle{}, ErrPluginNotActive
	}
	return pluginContext.runtime.supervisor.load(
		mountContext,
		pluginContext.ownerFiber,
		pluginContext.scope,
		instance,
	)
}

// Scope is a visibility and routing node. The Fiber, not Scope, owns visible
// Registry entries and Plugin lifecycle.
type Scope struct {
	runtime    *Runtime
	ownerFiber *fiber
	parent     *Scope
	target     ScopeKey
	label      string
	mutex      sync.RWMutex
	children   map[string]*Scope
	closed     bool
}

func newFiberRootScope(
	runtimeEngine *Runtime,
	ownerFiber *fiber,
	parentScope *Scope,
) *Scope {
	routingKey := ScopeKey{}
	if parentScope != nil {
		parentToken := parentScope.target.token
		scopeDepth := 1
		if parentToken != nil {
			scopeDepth = parentToken.depth + 1
		}
		routingKey = ScopeKey{
			token: &scopeToken{
				parent: parentToken,
				depth:  scopeDepth,
			},
		}
	}
	return &Scope{
		runtime:    runtimeEngine,
		ownerFiber: ownerFiber,
		parent:     parentScope,
		target:     routingKey,
		children:   make(map[string]*Scope),
	}
}

func newReplacementRootScope(
	runtimeEngine *Runtime,
	ownerFiber *fiber,
	previousScope *Scope,
) *Scope {
	return &Scope{
		runtime:    runtimeEngine,
		ownerFiber: ownerFiber,
		parent:     previousScope.parent,
		target:     previousScope.target,
		label:      previousScope.label,
		children:   make(map[string]*Scope),
	}
}

// Target returns the opaque routing identity of this Scope.
func (ownerScope *Scope) Target() ScopeKey {
	if ownerScope == nil {
		return ScopeKey{}
	}
	return ownerScope.target
}

func (ownerScope *Scope) depth() int {
	if ownerScope == nil || ownerScope.target.token == nil {
		return 0
	}
	return ownerScope.target.token.depth
}

func (ownerScope *Scope) isClosed() bool {
	if ownerScope == nil {
		return true
	}
	ownerScope.mutex.RLock()
	closed := ownerScope.closed
	ownerScope.mutex.RUnlock()
	return closed
}

func scopePath(sourceScope *Scope) []*Scope {
	lineage := make([]*Scope, 0)
	for currentScope := sourceScope; currentScope != nil; currentScope = currentScope.parent {
		lineage = append(lineage, currentScope)
	}
	return lineage
}

func (ownerScope *Scope) closeTree() {
	if ownerScope == nil {
		return
	}
	ownerScope.mutex.Lock()
	if ownerScope.closed {
		ownerScope.mutex.Unlock()
		return
	}
	ownerScope.closed = true
	children := make([]*Scope, 0, len(ownerScope.children))
	for _, nestedScope := range ownerScope.children {
		children = append(children, nestedScope)
	}
	ownerScope.mutex.Unlock()
	for _, nestedScope := range children {
		nestedScope.closeTree()
	}
}
