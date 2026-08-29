// Package execution owns the single active execution of one Agent.
package execution

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// State is the complete concurrency state of one Agent execution slot.
type State uint8

const (
	// StateIdle has no maintenance operation or Turn execution.
	StateIdle State = iota
	// StateMaintenance owns one cancelable maintenance operation.
	StateMaintenance
	// StateMaintenanceSettling closes maintenance without admitting other work.
	StateMaintenanceSettling
	// StateTurn owns one cancelable Turn operation.
	StateTurn
	// StateTurnSettling closes Turn facts and durability without exposing idle.
	StateTurnSettling
)

// Generation identifies one operation context admitted into the execution
// slot. A successor Turn receives a new generation without exposing idle.
type Generation uint64

// Cancellation is the stable cancellation fact retained by AgentExecution.
type Cancellation struct {
	// Kind identifies the canonical cancellation category.
	Kind string
	// Reason carries the optional Hook cancellation reason.
	Reason string
}

// ExecutionSnapshot is the immutable public state of one Agent execution slot.
type ExecutionSnapshot struct {
	// State is the current execution lifecycle state.
	State State
	// Generation identifies the active or most recently completed operation.
	Generation Generation
	// Cancellation is the first cancellation retained for the active operation.
	Cancellation Cancellation
}

// TurnEntry records how one Turn request changed the execution slot.
type TurnEntry struct {
	// Generation identifies the active execution after the request.
	Generation Generation
	// Entered is true when the request moved an idle slot into StateTurn.
	Entered bool
}

// AgentExecution owns exclusive execution, cancellation, wake latching, and
// idle convergence for one Agent.
type AgentExecution struct {
	mutex         sync.Mutex
	currentState  State
	generation    Generation
	operation     context.Context
	cancel        context.CancelCauseFunc
	cancellation  Cancellation
	wakeRequested bool
	done          chan struct{}
}

// New constructs one idle execution slot.
func New() *AgentExecution {
	completed := make(chan struct{})
	close(completed)
	return &AgentExecution{
		currentState: StateIdle,
		done:         completed,
	}
}

// EnterTurnOrRequestWake moves an idle slot into Turn execution or records that
// the active execution must consume successor work. The caller remains
// responsible for canceling an operation that was not entered.
func (owner *AgentExecution) EnterTurnOrRequestWake(
	requestContext context.Context,
	cancelOperation context.CancelCauseFunc,
) (TurnEntry, error) {
	if owner == nil {
		return TurnEntry{}, errors.New("agentloop execution: AgentExecution is nil")
	}
	if requestContext == nil || cancelOperation == nil {
		return TurnEntry{}, errors.New(
			"agentloop execution: Turn operation Context and cancellation are required",
		)
	}
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	switch owner.currentState {
	case StateIdle:
		owner.generation++
		owner.currentState = StateTurn
		owner.operation = requestContext
		owner.cancel = cancelOperation
		owner.cancellation = Cancellation{}
		owner.wakeRequested = false
		owner.done = make(chan struct{})
		return TurnEntry{
			Generation: owner.generation,
			Entered:    true,
		}, nil
	case StateMaintenance, StateMaintenanceSettling:
		if owner.cancellation.Kind != "disposed" {
			owner.wakeRequested = true
		}
		return TurnEntry{
			Generation: owner.generation,
		}, nil
	case StateTurn, StateTurnSettling:
		if owner.cancellation.Kind != "" && owner.cancellation.Kind != "disposed" {
			owner.wakeRequested = true
		}
		return TurnEntry{
			Generation: owner.generation,
		}, nil
	default:
		return TurnEntry{}, fmt.Errorf(
			"agentloop execution: enter Turn from state %d",
			owner.currentState,
		)
	}
}

// EnterMaintenance moves the idle execution slot into one maintenance
// operation.
func (owner *AgentExecution) EnterMaintenance(
	requestContext context.Context,
	cancelOperation context.CancelCauseFunc,
) (Generation, error) {
	if owner == nil {
		return 0, errors.New("agentloop execution: AgentExecution is nil")
	}
	if requestContext == nil || cancelOperation == nil {
		return 0, errors.New(
			"agentloop execution: maintenance Context and cancellation are required",
		)
	}
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	if owner.currentState != StateIdle || owner.wakeRequested {
		return 0, fmt.Errorf(
			"agentloop execution: enter maintenance from state %d",
			owner.currentState,
		)
	}
	owner.generation++
	owner.currentState = StateMaintenance
	owner.operation = requestContext
	owner.cancel = cancelOperation
	owner.cancellation = Cancellation{}
	owner.done = make(chan struct{})
	return owner.generation, nil
}

// RecordCancellation records the first cancellation and stops the active
// operation. When keepWake is false, a previously requested successor wake is
// discarded.
func (owner *AgentExecution) RecordCancellation(
	detail Cancellation,
	problem error,
	keepWake bool,
) bool {
	if owner == nil {
		return false
	}
	owner.mutex.Lock()
	if owner.currentState == StateIdle {
		owner.mutex.Unlock()
		return false
	}
	if !keepWake {
		owner.wakeRequested = false
	}
	if owner.cancellation.Kind == "" {
		owner.cancellation = detail
	}
	cancelOperation := owner.cancel
	owner.mutex.Unlock()
	if cancelOperation != nil {
		cancelOperation(problem)
	}
	return true
}

// EnterTurnSettling moves an admitted Turn out of operation and into its
// non-cancelable settlement boundary.
func (owner *AgentExecution) EnterTurnSettling(selected Generation) error {
	return owner.enterSettlement(
		selected,
		StateTurn,
		StateTurnSettling,
		"Turn",
	)
}

// EnterSuccessorTurn installs a fresh operation Context for a successor Turn
// without closing the shared completion signal.
func (owner *AgentExecution) EnterSuccessorTurn(
	selected Generation,
	requestContext context.Context,
	cancelOperation context.CancelCauseFunc,
) (Generation, error) {
	if owner == nil {
		return 0, errors.New("agentloop execution: AgentExecution is nil")
	}
	if requestContext == nil || cancelOperation == nil {
		return 0, errors.New(
			"agentloop execution: successor Turn Context and cancellation are required",
		)
	}
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	if owner.currentState != StateTurnSettling || owner.generation != selected {
		return 0, owner.transitionError("enter successor Turn", selected)
	}
	if owner.cancel != nil {
		owner.cancel(nil)
	}
	owner.generation++
	owner.currentState = StateTurn
	owner.operation = requestContext
	owner.cancel = cancelOperation
	owner.cancellation = Cancellation{}
	owner.wakeRequested = false
	return owner.generation, nil
}

// EnterMaintenanceSettling moves maintenance into settlement.
func (owner *AgentExecution) EnterMaintenanceSettling(
	selected Generation,
) error {
	return owner.enterSettlement(
		selected,
		StateMaintenance,
		StateMaintenanceSettling,
		"maintenance",
	)
}

// EnterTurnAfterMaintenance starts a requested Turn without exposing an idle
// convergence point.
func (owner *AgentExecution) EnterTurnAfterMaintenance(
	selected Generation,
	requestContext context.Context,
	cancelOperation context.CancelCauseFunc,
) (Generation, error) {
	if owner == nil {
		return 0, errors.New("agentloop execution: AgentExecution is nil")
	}
	if requestContext == nil || cancelOperation == nil {
		return 0, errors.New(
			"agentloop execution: successor Turn Context and cancellation are required",
		)
	}
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	if owner.currentState != StateMaintenanceSettling ||
		owner.generation != selected || !owner.wakeRequested {
		return 0, owner.transitionError(
			"enter Turn after maintenance",
			selected,
		)
	}
	if owner.cancel != nil {
		owner.cancel(nil)
	}
	owner.generation++
	owner.currentState = StateTurn
	owner.operation = requestContext
	owner.cancel = cancelOperation
	owner.cancellation = Cancellation{}
	owner.wakeRequested = false
	return owner.generation, nil
}

// EnterIdle returns a settled Turn or maintenance operation to idle and closes
// the convergence signal shared by the completed execution chain.
func (owner *AgentExecution) EnterIdle(selected Generation) error {
	if owner == nil {
		return errors.New("agentloop execution: AgentExecution is nil")
	}
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	if owner.generation != selected ||
		(owner.currentState != StateTurnSettling &&
			owner.currentState != StateMaintenanceSettling) {
		return owner.transitionError("enter idle", selected)
	}
	if owner.cancel != nil {
		owner.cancel(nil)
	}
	owner.currentState = StateIdle
	owner.operation = nil
	owner.cancel = nil
	owner.cancellation = Cancellation{}
	owner.wakeRequested = false
	close(owner.done)
	return nil
}

// Snapshot returns an immutable execution view without exposing the operation
// Context or cancellation function.
func (owner *AgentExecution) Snapshot() ExecutionSnapshot {
	if owner == nil {
		return ExecutionSnapshot{
			State: StateIdle,
		}
	}
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	return ExecutionSnapshot{
		State:        owner.currentState,
		Generation:   owner.generation,
		Cancellation: owner.cancellation,
	}
}

// OperationContext returns the Context owned by the selected active execution.
// A nil result means the generation is stale or the slot has no operation.
func (owner *AgentExecution) OperationContext(
	selected Generation,
) context.Context {
	if owner == nil {
		return nil
	}
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	if owner.generation != selected {
		return nil
	}
	return owner.operation
}

// IdleWait returns whether the slot is converged and the signal for the active
// connected execution chain.
func (owner *AgentExecution) IdleWait() (bool, <-chan struct{}) {
	if owner == nil {
		completed := make(chan struct{})
		close(completed)
		return true, completed
	}
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	idle := owner.currentState == StateIdle && !owner.wakeRequested
	return idle, owner.done
}

// Running reports the externally visible running status.
func (owner *AgentExecution) Running() bool {
	if owner == nil {
		return false
	}
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	return owner.currentState == StateTurn ||
		owner.currentState == StateTurnSettling
}

// WakeRequested reports whether successor work was latched.
func (owner *AgentExecution) WakeRequested(selected Generation) bool {
	if owner == nil {
		return false
	}
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	return owner.generation == selected && owner.wakeRequested
}

// DiscardWakeRequest prevents disposal from starting successor work.
func (owner *AgentExecution) DiscardWakeRequest() {
	if owner == nil {
		return
	}
	owner.mutex.Lock()
	owner.wakeRequested = false
	owner.mutex.Unlock()
}

func (owner *AgentExecution) enterSettlement(
	selected Generation,
	source State,
	destination State,
	label string,
) error {
	if owner == nil {
		return errors.New("agentloop execution: AgentExecution is nil")
	}
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	if owner.currentState != source || owner.generation != selected {
		return owner.transitionError("enter "+label+" settling", selected)
	}
	owner.currentState = destination
	return nil
}

func (owner *AgentExecution) transitionError(
	action string,
	selected Generation,
) error {
	return fmt.Errorf(
		"agentloop execution: cannot %s generation %d from state %d generation %d",
		action,
		selected,
		owner.currentState,
		owner.generation,
	)
}
