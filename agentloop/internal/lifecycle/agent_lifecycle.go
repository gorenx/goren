// Package lifecycle owns Agent API invocation admission and close cutoff.
package lifecycle

import (
	"errors"
	"fmt"
	"sync"
)

// State is the complete Agent invocation-admission lifecycle.
type State uint8

const (
	// StateConstructed has not opened Agent invocation admission.
	StateConstructed State = iota
	// StateServing admits Agent invocations until the close cutoff.
	StateServing
	// StateClosing rejects new invocations and drains admitted invocations.
	StateClosing
	// StateClosed records completed Agent invocation shutdown.
	StateClosed
)

// AgentInvocation identifies one Agent API invocation admitted before the
// closing cutoff.
type AgentInvocation struct {
	identifier uint64
}

// AgentLifecycle owns Agent API invocation settlement and the closing cutoff.
type AgentLifecycle struct {
	mutex          sync.Mutex
	currentState   State
	nextInvocation uint64
	// activeInvocations is the set admitted before the closing cutoff. The key
	// is a monotonically increasing invocation identifier. The empty value is a
	// membership marker: presence means the invocation has not finished;
	// absence means it was never admitted or has already finished.
	activeInvocations  map[uint64]struct{}
	invocationsDrained chan struct{}
	drainedClosed      bool
}

// New constructs one Agent lifecycle before it becomes live.
func New() *AgentLifecycle {
	return &AgentLifecycle{
		currentState:       StateConstructed,
		activeInvocations:  make(map[uint64]struct{}),
		invocationsDrained: make(chan struct{}),
	}
}

// EnterServing moves a constructed Agent into service and opens invocation
// admission after Agent membership commits.
func (owner *AgentLifecycle) EnterServing() error {
	if owner == nil {
		return errors.New("agentloop lifecycle: Agent lifecycle is nil")
	}
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	if owner.currentState != StateConstructed {
		return fmt.Errorf(
			"agentloop lifecycle: enter serving from state %d",
			owner.currentState,
		)
	}
	owner.currentState = StateServing
	return nil
}

// AdmitInvocation admits one Agent API invocation before the closing cutoff.
func (owner *AgentLifecycle) AdmitInvocation() (AgentInvocation, error) {
	if owner == nil {
		return AgentInvocation{}, errors.New(
			"agentloop lifecycle: Agent lifecycle is nil",
		)
	}
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	if owner.currentState != StateServing {
		return AgentInvocation{}, fmt.Errorf(
			"agentloop lifecycle: Agent is not serving (state %d)",
			owner.currentState,
		)
	}
	owner.nextInvocation++
	admitted := AgentInvocation{
		identifier: owner.nextInvocation,
	}
	owner.activeInvocations[admitted.identifier] = struct{}{}
	return admitted, nil
}

// FinishInvocation finishes one exact admitted Agent API invocation.
func (owner *AgentLifecycle) FinishInvocation(admitted AgentInvocation) error {
	if owner == nil {
		return errors.New("agentloop lifecycle: Agent lifecycle is nil")
	}
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	if admitted.identifier == 0 {
		return errors.New("agentloop lifecycle: Agent invocation is empty")
	}
	if _, found := owner.activeInvocations[admitted.identifier]; !found {
		return fmt.Errorf(
			"agentloop lifecycle: Agent invocation %d is not active",
			admitted.identifier,
		)
	}
	delete(owner.activeInvocations, admitted.identifier)
	owner.closeDrainedLocked()
	return nil
}

// EnterClosing moves the Agent to closing, rejects new invocations, and returns
// a signal that closes after all invocations admitted before the cutoff finish.
func (owner *AgentLifecycle) EnterClosing() <-chan struct{} {
	if owner == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	switch owner.currentState {
	case StateConstructed, StateServing:
		owner.currentState = StateClosing
		owner.closeDrainedLocked()
	case StateClosing, StateClosed:
	}
	return owner.invocationsDrained
}

// EnterClosed records that execution settlement and structural release
// completed after admitted invocations drained, then moves the Agent to closed.
func (owner *AgentLifecycle) EnterClosed() error {
	if owner == nil {
		return errors.New("agentloop lifecycle: Agent lifecycle is nil")
	}
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	if owner.currentState == StateClosed {
		return nil
	}
	if owner.currentState != StateClosing {
		return fmt.Errorf(
			"agentloop lifecycle: enter closed from state %d",
			owner.currentState,
		)
	}
	if len(owner.activeInvocations) != 0 {
		return errors.New(
			"agentloop lifecycle: enter closed while Agent invocations remain active",
		)
	}
	owner.currentState = StateClosed
	return nil
}

// StateValue returns the current lifecycle state.
func (owner *AgentLifecycle) StateValue() State {
	if owner == nil {
		return StateClosed
	}
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	return owner.currentState
}

func (owner *AgentLifecycle) closeDrainedLocked() {
	if owner.currentState != StateClosing || len(owner.activeInvocations) != 0 ||
		owner.drainedClosed {
		return
	}
	close(owner.invocationsDrained)
	owner.drainedClosed = true
}
