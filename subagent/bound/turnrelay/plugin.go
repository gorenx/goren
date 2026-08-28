// Package turnrelay relays completed user Session turns into Bound Inboxes.
package turnrelay

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/gorenx/goren/agent"
	pluginruntime "github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	boundcontract "github.com/gorenx/goren/subagent/bound"
)

const PluginName = "@gorenx/goren-bound-turn-relay"

// Diagnostics supplies process-owned reporting for contained relay failures.
type Diagnostics struct {
	WorkerError func(error)
}

type relayKey struct {
	sessionID session.SessionID
	name      string
}

// Plugin owns Session observation, source cursors, and one relay worker per
// live user-Session Binding. Bound owns only the target Inbox.
type Plugin struct {
	pluginruntime.Base

	report func(error)
	agents agent.Registry
	store  session.LiveStore
	inbox  boundcontract.Inbox
	ctx    context.Context
	cancel context.CancelFunc
	mutex  sync.Mutex
	// Key is a user Session ID plus Bound name. Value is the exact relay worker
	// owned by the current parent Agent epoch for that durable Binding.
	workers map[relayKey]*worker
	tasks   sync.WaitGroup
	closing chan struct{}
}

// New constructs an inactive Turn Relay Plugin.
func New(hooks Diagnostics) *Plugin {
	reporter := hooks.WorkerError
	if reporter == nil {
		reporter = func(error) {}
	}
	return &Plugin{
		report:  reporter,
		workers: make(map[relayKey]*worker),
	}
}

// Manifest declares the input source's dependencies and observations. It
// provides no Bound capability of its own.
func (owner *Plugin) Manifest() pluginruntime.Manifest {
	return pluginruntime.Manifest{
		Name: PluginName,
		Requires: []pluginruntime.ServiceType{
			pluginruntime.ServiceOf[agent.Registry](),
			pluginruntime.ServiceOf[session.LiveStore](),
			pluginruntime.ServiceOf[boundcontract.Inbox](),
		},
		Events: []pluginruntime.EventSubscription{
			pluginruntime.EventOf[agent.SessionStarted](),
			pluginruntime.EventOf[agent.Disposed](),
			pluginruntime.EventOf[session.EventAppended](),
		},
	}
}

// Apply resolves the target Inbox and starts the relay lifecycle owner.
func (owner *Plugin) Apply(ctx context.Context) error {
	if ctx == nil {
		return errors.New("subagent/bound/turnrelay: Apply Context is nil")
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	agentRegistry, err := pluginruntime.Require[agent.Registry](owner)
	if err != nil {
		return err
	}
	store, err := pluginruntime.Require[session.LiveStore](owner)
	if err != nil {
		return err
	}
	targetInbox, err := pluginruntime.Require[boundcontract.Inbox](owner)
	if err != nil {
		return err
	}
	lifecycleContext, cancelLifecycle := context.WithCancel(
		context.WithoutCancel(ctx),
	)
	owner.mutex.Lock()
	if owner.ctx != nil || owner.closing != nil {
		owner.mutex.Unlock()
		cancelLifecycle()
		return errors.New("subagent/bound/turnrelay: Plugin is unavailable")
	}
	owner.agents = agentRegistry
	owner.store = store
	owner.inbox = targetInbox
	owner.ctx = lifecycleContext
	owner.cancel = cancelLifecycle
	owner.mutex.Unlock()
	for _, subject := range agentRegistry.List() {
		owner.observeSession(subject)
	}
	return nil
}

// ObserveEvent performs only in-memory worker reconciliation and wakeup.
func (owner *Plugin) ObserveEvent(
	_ context.Context,
	fact pluginruntime.Event,
) error {
	switch observed := fact.(type) {
	case agent.SessionStarted:
		owner.observeSession(observed.Subject)
	case agent.Disposed:
		owner.stopParent(observed.Subject)
	case session.EventAppended:
		if observed.Conversation == nil {
			return nil
		}
		switch observed.Committed.Type {
		case boundcontract.BindingEventName:
		case session.TurnEndEventName:
		default:
			return nil
		}
		owner.mutex.Lock()
		agentRegistry := owner.agents
		owner.mutex.Unlock()
		if agentRegistry == nil {
			return nil
		}
		parentAgent, found := agentRegistry.Get(observed.Conversation.ID())
		if !found {
			return nil
		}
		owner.observeSession(parentAgent)
	}
	return nil
}

func (owner *Plugin) observeSession(parentAgent agent.Agent) {
	if !directUserAgent(parentAgent) {
		return
	}
	bindings, err := sessionBindings(
		parentAgent.SessionValue().Events(),
		parentAgent.ID(),
	)
	if err != nil {
		owner.report(fmt.Errorf(
			"subagent/bound/turnrelay: inspect user Session %q: %w",
			parentAgent.ID(),
			err,
		))
		return
	}
	owner.ensureWorkers(parentAgent, bindings)
}

func (owner *Plugin) ensureWorkers(
	parentAgent agent.Agent,
	bindings []binding,
) {
	owner.mutex.Lock()
	if owner.ctx == nil || owner.store == nil ||
		owner.inbox == nil {
		owner.mutex.Unlock()
		return
	}
	for key, current := range owner.workers {
		if key.sessionID != parentAgent.ID() ||
			agent.Same(current.parent, parentAgent) {
			continue
		}
		delete(owner.workers, key)
		current.cancel()
	}
	started := make([]*worker, 0, len(bindings))
	for _, bindingValue := range bindings {
		key := relayKey{
			sessionID: bindingValue.address.SessionID,
			name:      bindingValue.address.Name,
		}
		if current := owner.workers[key]; current != nil {
			current.wake()
			continue
		}
		workerContext, cancelWorker := context.WithCancel(owner.ctx)
		current := newWorker(
			owner,
			workerContext,
			cancelWorker,
			parentAgent,
			owner.store,
			owner.inbox,
			bindingValue,
		)
		owner.workers[key] = current
		owner.tasks.Add(1)
		started = append(started, current)
	}
	owner.mutex.Unlock()
	for _, current := range started {
		go current.run()
	}
}

func (owner *Plugin) stopParent(parentAgent agent.Agent) {
	if parentAgent == nil {
		return
	}
	owner.mutex.Lock()
	for key, current := range owner.workers {
		if key.sessionID != parentAgent.ID() ||
			!agent.Same(current.parent, parentAgent) {
			continue
		}
		delete(owner.workers, key)
		current.cancel()
	}
	owner.mutex.Unlock()
}

func (owner *Plugin) workerClosed(current *worker) {
	key := relayKey{
		sessionID: current.binding.address.SessionID,
		name:      current.binding.address.Name,
	}
	owner.mutex.Lock()
	if owner.workers[key] == current {
		delete(owner.workers, key)
	}
	owner.mutex.Unlock()
	owner.tasks.Done()
}

// Dispose cancels every relay and waits for their source work to stop.
func (owner *Plugin) Dispose(closeContext context.Context) error {
	if closeContext == nil {
		closeContext = context.Background()
	}
	owner.mutex.Lock()
	if owner.ctx == nil {
		closing := owner.closing
		owner.mutex.Unlock()
		return waitForClose(closeContext, closing)
	}
	cancelLifecycle := owner.cancel
	closing := make(chan struct{})
	owner.closing = closing
	owner.cancel = nil
	owner.ctx = nil
	owner.agents = nil
	owner.store = nil
	owner.inbox = nil
	owner.mutex.Unlock()
	if cancelLifecycle != nil {
		cancelLifecycle()
	}
	go func() {
		owner.tasks.Wait()
		owner.mutex.Lock()
		if owner.closing == closing {
			owner.closing = nil
		}
		close(closing)
		owner.mutex.Unlock()
	}()
	return waitForClose(closeContext, closing)
}

func waitForClose(
	closeContext context.Context,
	closing <-chan struct{},
) error {
	if closing == nil {
		return nil
	}
	select {
	case <-closing:
		return nil
	case <-closeContext.Done():
		return context.Cause(closeContext)
	}
}

func directUserAgent(subject agent.Agent) bool {
	if subject == nil || subject.SessionValue() == nil {
		return false
	}
	header := subject.SessionValue().Header()
	return header.Origin == "" && header.ParentSession == nil
}

var _ pluginruntime.Plugin = (*Plugin)(nil)
var _ pluginruntime.EventObserver = (*Plugin)(nil)
