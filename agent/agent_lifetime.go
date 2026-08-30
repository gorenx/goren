package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/gorenx/goren/session"
)

type agentLifetimeState uint8

const (
	// lifetimeConstructing reserves a Session ID while Factory builds the Host.
	lifetimeConstructing agentLifetimeState = iota
	// lifetimeAttached owns a Host that has not completed Agent publication.
	lifetimeAttached
	// lifetimePublishing is visible while the Created event is being observed.
	lifetimePublishing
	// lifetimeStarting has published Created and is completing startup events.
	lifetimeStarting
	// lifetimeLive accepts Scope operations and runtime descendants.
	lifetimeLive
	// lifetimeClosingUnpublished closes an Agent that never published Created.
	lifetimeClosingUnpublished
	// lifetimeClosingPublishing waits for the admitted Created dispatch to end.
	lifetimeClosingPublishing
	// lifetimeClosingPublished requires one paired Disposed dispatch.
	lifetimeClosingPublished
	// lifetimeRetired has attempted Disposed and is closing the owned Host.
	lifetimeRetired
	// lifetimeClosed is terminal and absent from Registry indexes.
	lifetimeClosed
)

type lifetimeCloseAdmission uint8

const (
	// lifetimeCloseStarted assigns the close transaction to this caller.
	lifetimeCloseStarted lifetimeCloseAdmission = iota
	// lifetimeCloseRunning joins an already active close transaction.
	lifetimeCloseRunning
	// lifetimeCloseFinished returns the stable result of a closed lifetime.
	lifetimeCloseFinished
)

// agentLifetime is the state owner for one exact in-process Agent lifetime.
// Registry owns only its index membership and parent-child ID relations.
type agentLifetime struct {
	mutex        sync.Mutex
	id           session.SessionID
	ownedHost    Host
	state        agentLifetimeState
	construction lifecycleSignal
	closing      lifecycleSignal
	closed       lifecycleSignal
	closeErr     error
	dispatches   int
}

func newAgentLifetime(identifier session.SessionID) *agentLifetime {
	return &agentLifetime{
		id:           identifier,
		state:        lifetimeConstructing,
		construction: newLifecycleSignal(),
		closing:      newLifecycleSignal(),
		closed:       newLifecycleSignal(),
	}
}

func (lifetime *agentLifetime) SessionID() session.SessionID {
	return lifetime.id
}

func (lifetime *agentLifetime) Agent() Agent {
	lifetime.mutex.Lock()
	defer lifetime.mutex.Unlock()
	if lifetime.ownedHost == nil {
		return nil
	}
	return lifetime.ownedHost.Agent()
}

func (lifetime *agentLifetime) Matches(subject Agent) bool {
	if subject == nil {
		return false
	}
	lifetime.mutex.Lock()
	defer lifetime.mutex.Unlock()
	return lifetime.ownedHost != nil &&
		Same(lifetime.ownedHost.Agent(), subject)
}

func (lifetime *agentLifetime) Attach(agentHost Host) error {
	if agentHost == nil || agentHost.Agent() == nil || agentHost.Scope() == nil {
		return errors.New("agent: Factory returned an incomplete Agent Host")
	}
	if agentHost.Agent().ID() != lifetime.id {
		return fmt.Errorf(
			"agent: Agent id %q does not match reserved id %q",
			agentHost.Agent().ID(),
			lifetime.id,
		)
	}
	lifetime.mutex.Lock()
	defer lifetime.mutex.Unlock()
	if lifetime.state != lifetimeConstructing {
		return errors.New("agent: Agent construction is no longer active")
	}
	lifetime.ownedHost = agentHost
	lifetime.state = lifetimeAttached
	return nil
}

func (lifetime *agentLifetime) ApplySetup(
	requestContext context.Context,
	contribution Setup,
) (ScopeResources, error) {
	lifetime.mutex.Lock()
	if lifetime.state != lifetimeLive && lifetime.state != lifetimeAttached {
		lifetime.mutex.Unlock()
		return nil, errors.New("agent: exact Agent is not accepting Setup")
	}
	agentHost := lifetime.ownedHost
	if agentHost == nil || agentHost.Agent() == nil || agentHost.Scope() == nil {
		lifetime.mutex.Unlock()
		return nil, errors.New("agent: exact Agent Scope is unavailable")
	}
	subject := agentHost.Agent()
	agentScope := agentHost.Scope()
	lifetime.mutex.Unlock()
	return agentScope.ApplySetup(requestContext, subject, contribution)
}

func (lifetime *agentLifetime) AcceptsDescendants() bool {
	lifetime.mutex.Lock()
	defer lifetime.mutex.Unlock()
	return lifetime.visibleLocked()
}

func (lifetime *agentLifetime) AcceptsSetup() bool {
	lifetime.mutex.Lock()
	defer lifetime.mutex.Unlock()
	return lifetime.state == lifetimeLive
}

func (lifetime *agentLifetime) Visible() bool {
	lifetime.mutex.Lock()
	defer lifetime.mutex.Unlock()
	return lifetime.visibleLocked()
}

func (lifetime *agentLifetime) visibleLocked() bool {
	if lifetime.ownedHost == nil || lifetime.ownedHost.Agent() == nil {
		return false
	}
	return lifetime.state == lifetimePublishing ||
		lifetime.state == lifetimeStarting ||
		lifetime.state == lifetimeLive
}

func (lifetime *agentLifetime) BeginPublication() error {
	lifetime.mutex.Lock()
	defer lifetime.mutex.Unlock()
	if lifetime.state != lifetimeAttached {
		return errors.New("agent: Agent is not ready for publication")
	}
	lifetime.state = lifetimePublishing
	return nil
}

func (lifetime *agentLifetime) CompleteCreatedEvent() error {
	lifetime.mutex.Lock()
	defer lifetime.mutex.Unlock()
	switch lifetime.state {
	case lifetimePublishing:
		lifetime.state = lifetimeStarting
		return nil
	case lifetimeClosingPublishing:
		lifetime.state = lifetimeClosingPublished
		return errors.New("agent: Agent publication was interrupted")
	default:
		return errors.New("agent: Created event is not active")
	}
}

func (lifetime *agentLifetime) EnterLive() error {
	lifetime.mutex.Lock()
	defer lifetime.mutex.Unlock()
	if lifetime.state != lifetimeStarting {
		return errors.New("agent: Agent publication was interrupted")
	}
	lifetime.state = lifetimeLive
	return nil
}

func (lifetime *agentLifetime) DispatchLifecycleEvent(
	requestContext context.Context,
	fact AgentEvent,
) error {
	lifetime.mutex.Lock()
	agentHost := lifetime.ownedHost
	if agentHost == nil || lifetime.state == lifetimeClosed {
		lifetime.mutex.Unlock()
		return errors.New("agent: exact Agent event Scope is unavailable")
	}
	agentScope := agentHost.Scope()
	if agentScope == nil {
		lifetime.mutex.Unlock()
		return errors.New("agent: exact Agent event Scope is unavailable")
	}
	lifetime.dispatches++
	lifetime.mutex.Unlock()
	defer lifetime.finishDispatch()
	return agentScope.Dispatch(requestContext, fact)
}

func (lifetime *agentLifetime) DispatchEvent(
	requestContext context.Context,
	subject Agent,
	fact AgentEvent,
) error {
	lifetime.mutex.Lock()
	agentHost := lifetime.ownedHost
	available := agentHost != nil && Same(agentHost.Agent(), subject)
	var agentScope Scope
	if available {
		agentScope = agentHost.Scope()
		available = agentScope != nil
	}
	if available && lifetime.closingLocked() {
		_, available = fact.(AgentClosingEvent)
	} else {
		available = available && lifetime.state == lifetimeLive
	}
	if !available {
		lifetime.mutex.Unlock()
		return errors.New("agent: exact Agent event Scope is unavailable")
	}
	lifetime.dispatches++
	lifetime.mutex.Unlock()
	defer lifetime.finishDispatch()
	return agentScope.Dispatch(requestContext, fact)
}

func (lifetime *agentLifetime) finishDispatch() {
	lifetime.mutex.Lock()
	lifetime.dispatches--
	lifetime.mutex.Unlock()
}

func (lifetime *agentLifetime) Dispatching() bool {
	lifetime.mutex.Lock()
	defer lifetime.mutex.Unlock()
	return lifetime.dispatches != 0
}

func (lifetime *agentLifetime) BeginClose() lifetimeCloseAdmission {
	lifetime.mutex.Lock()
	defer lifetime.mutex.Unlock()
	switch lifetime.state {
	case lifetimeClosed:
		return lifetimeCloseFinished
	case lifetimeClosingUnpublished,
		lifetimeClosingPublishing,
		lifetimeClosingPublished,
		lifetimeRetired:
		return lifetimeCloseRunning
	case lifetimePublishing:
		lifetime.state = lifetimeClosingPublishing
	case lifetimeStarting, lifetimeLive:
		lifetime.state = lifetimeClosingPublished
	default:
		lifetime.state = lifetimeClosingUnpublished
	}
	lifetime.closing.close()
	return lifetimeCloseStarted
}

func (lifetime *agentLifetime) closingLocked() bool {
	return lifetime.state == lifetimeClosingUnpublished ||
		lifetime.state == lifetimeClosingPublishing ||
		lifetime.state == lifetimeClosingPublished ||
		lifetime.state == lifetimeRetired
}

func (lifetime *agentLifetime) BeginRetirement() (Agent, bool) {
	lifetime.mutex.Lock()
	defer lifetime.mutex.Unlock()
	if lifetime.state != lifetimeClosingPublished ||
		lifetime.ownedHost == nil || lifetime.ownedHost.Scope() == nil {
		return nil, false
	}
	lifetime.state = lifetimeRetired
	return lifetime.ownedHost.Agent(), true
}

func (lifetime *agentLifetime) EnterClosed(closeErr error) bool {
	lifetime.mutex.Lock()
	defer lifetime.mutex.Unlock()
	if lifetime.state == lifetimeClosed {
		return false
	}
	lifetime.state = lifetimeClosed
	lifetime.closeErr = closeErr
	lifetime.closed.close()
	return true
}

func (lifetime *agentLifetime) IsClosed() bool {
	lifetime.mutex.Lock()
	defer lifetime.mutex.Unlock()
	return lifetime.state == lifetimeClosed
}

func (lifetime *agentLifetime) CloseResult() error {
	lifetime.mutex.Lock()
	defer lifetime.mutex.Unlock()
	return lifetime.closeErr
}

func (lifetime *agentLifetime) CloseOwnedHost(
	closeContext context.Context,
) (bool, error) {
	lifetime.mutex.Lock()
	agentHost := lifetime.ownedHost
	lifetime.mutex.Unlock()
	if agentHost == nil {
		return false, nil
	}
	return true, agentHost.Close(closeContext)
}

func (lifetime *agentLifetime) FinishConstruction() {
	lifetime.construction.close()
}

func (lifetime *agentLifetime) ConstructionDone() <-chan struct{} {
	return lifetime.construction.Done()
}

func (lifetime *agentLifetime) ClosingSignal() <-chan struct{} {
	return lifetime.closing.Done()
}

func (lifetime *agentLifetime) ClosedSignal() <-chan struct{} {
	return lifetime.closed.Done()
}
