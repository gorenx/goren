// Package toolbatch owns model-order progression for one Tool call batch.
package toolbatch

import (
	"errors"
	"fmt"
)

// State is the complete lifecycle of one ToolBatch.
type State uint8

const (
	// StatePlanned has not admitted a Tool call.
	StatePlanned State = iota
	// StateDispatching admits calls and accepts settled results in model order.
	StateDispatching
	// StateDraining stops new dispatch after cancellation or scheduler failure.
	StateDraining
	// StateSettling verifies all required Tool results were accepted.
	StateSettling
	// StateClosed has completed the ToolBatch boundary.
	StateClosed
)

// DrainReason distinguishes cancellation settlement from scheduler failure.
type DrainReason uint8

const (
	// DrainNone records that dispatch has not been interrupted.
	DrainNone DrainReason = iota
	// DrainCancellation requires synthetic pairs for calls not dispatched.
	DrainCancellation
	// DrainFailure forbids fabricated results after scheduler failure.
	DrainFailure
)

// ToolBatch owns started, settled, and model-order accepted call positions.
type ToolBatch struct {
	currentState           State
	drainReason            DrainReason
	limit                  int
	started                []bool
	settled                []bool
	accepted               []bool
	nextToStart            int
	nextToAccept           int
	inFlight               int
	stopsModelContinuation bool
}

// New constructs one planned Tool call batch.
func New(callCount int, parallelLimit int) (*ToolBatch, error) {
	if callCount <= 0 {
		return nil, errors.New("agentloop ToolBatch: call count must be positive")
	}
	if parallelLimit <= 0 {
		return nil, errors.New("agentloop ToolBatch: parallel limit must be positive")
	}
	return &ToolBatch{
		currentState: StatePlanned,
		limit:        parallelLimit,
		started:      make([]bool, callCount),
		settled:      make([]bool, callCount),
		accepted:     make([]bool, callCount),
	}, nil
}

// EnterDispatching moves a planned ToolBatch into call dispatch.
func (current *ToolBatch) EnterDispatching() error {
	if current == nil {
		return errors.New("agentloop ToolBatch: ToolBatch is nil")
	}
	if current.currentState != StatePlanned {
		return current.transitionError("enter dispatching", current.nextToStart)
	}
	current.currentState = StateDispatching
	return nil
}

// CanStart reports whether the next model-order call can enter dispatch.
func (current *ToolBatch) CanStart() bool {
	return current != nil &&
		current.currentState == StateDispatching &&
		current.nextToStart < len(current.started) &&
		current.inFlight < current.limit
}

// RecordCallStart records a call start and reserves one in-flight position
// without changing the batch lifecycle state.
func (current *ToolBatch) RecordCallStart(index int) error {
	if current == nil {
		return errors.New("agentloop ToolBatch: ToolBatch is nil")
	}
	if !current.CanStart() || index != current.nextToStart {
		return current.transitionError("start call", index)
	}
	current.started[index] = true
	current.nextToStart++
	current.inFlight++
	return nil
}

// RecordCallSettlement records that one started body or immediate policy result
// completed without changing the batch lifecycle state.
func (current *ToolBatch) RecordCallSettlement(index int) error {
	if current == nil {
		return errors.New("agentloop ToolBatch: ToolBatch is nil")
	}
	if index < 0 || index >= len(current.started) || !current.started[index] ||
		current.settled[index] {
		return current.transitionError("settle call", index)
	}
	if current.currentState != StateDispatching &&
		current.currentState != StateDraining {
		return current.transitionError("settle call", index)
	}
	current.settled[index] = true
	current.inFlight--
	return nil
}

// RecordAcceptedResult records a committed real Tool result in model order.
func (current *ToolBatch) RecordAcceptedResult(
	index int,
	stopContinuation bool,
) error {
	if current == nil {
		return errors.New("agentloop ToolBatch: ToolBatch is nil")
	}
	if current.currentState != StateDispatching &&
		current.currentState != StateDraining {
		return current.transitionError("accept call", index)
	}
	if index != current.nextToAccept || index < 0 ||
		index >= len(current.started) || !current.started[index] ||
		!current.settled[index] || current.accepted[index] {
		return current.transitionError("accept call", index)
	}
	current.accepted[index] = true
	current.nextToAccept++
	current.stopsModelContinuation = current.stopsModelContinuation ||
		stopContinuation
	return nil
}

// EnterDraining stops new calls after cancellation or scheduler failure.
func (current *ToolBatch) EnterDraining(reason DrainReason) error {
	if current == nil {
		return errors.New("agentloop ToolBatch: ToolBatch is nil")
	}
	if current.currentState != StateDispatching {
		return current.transitionError("enter draining", current.nextToStart)
	}
	if reason != DrainCancellation && reason != DrainFailure {
		return errors.New("agentloop ToolBatch: drain reason is invalid")
	}
	current.currentState = StateDraining
	current.drainReason = reason
	return nil
}

// RecordSkippedResult records one canonical synthetic call/result pair after
// cancellation. Scheduler failure cannot use this operation.
func (current *ToolBatch) RecordSkippedResult(index int) error {
	if current == nil {
		return errors.New("agentloop ToolBatch: ToolBatch is nil")
	}
	if current.currentState != StateDraining ||
		current.drainReason != DrainCancellation || index != current.nextToAccept ||
		index != current.nextToStart || index < 0 || index >= len(current.started) {
		return current.transitionError("accept skipped call", index)
	}
	current.accepted[index] = true
	current.nextToStart++
	current.nextToAccept++
	return nil
}

// EnterSettling verifies the batch-specific completion rule before
// ordered result settlement closes.
func (current *ToolBatch) EnterSettling() error {
	if current == nil {
		return errors.New("agentloop ToolBatch: ToolBatch is nil")
	}
	if current.currentState != StateDispatching &&
		current.currentState != StateDraining {
		return current.transitionError("enter settling", current.nextToAccept)
	}
	if current.inFlight != 0 {
		return errors.New("agentloop ToolBatch: settle with in-flight calls")
	}
	if current.drainReason != DrainFailure &&
		current.nextToAccept != len(current.accepted) {
		return errors.New("agentloop ToolBatch: settle before all results were accepted")
	}
	current.currentState = StateSettling
	return nil
}

// EnterClosed records completion of ordered ToolBatch settlement.
func (current *ToolBatch) EnterClosed() error {
	if current == nil {
		return errors.New("agentloop ToolBatch: ToolBatch is nil")
	}
	if current.currentState != StateSettling {
		return current.transitionError("close", current.nextToAccept)
	}
	current.currentState = StateClosed
	return nil
}

// StopsModelContinuation reports whether an accepted Tool result suppresses the
// automatic model request that would otherwise consume Tool results.
func (current *ToolBatch) StopsModelContinuation() bool {
	return current != nil && current.stopsModelContinuation
}

// StateValue returns the current ToolBatch state.
func (current *ToolBatch) StateValue() State {
	if current == nil {
		return StateClosed
	}
	return current.currentState
}

// DrainReasonValue returns why the batch stopped dispatching.
func (current *ToolBatch) DrainReasonValue() DrainReason {
	if current == nil {
		return DrainFailure
	}
	return current.drainReason
}

func (current *ToolBatch) transitionError(action string, index int) error {
	return fmt.Errorf(
		"agentloop ToolBatch: cannot %s at index %d from state %d (start %d, accept %d)",
		action,
		index,
		current.currentState,
		current.nextToStart,
		current.nextToAccept,
	)
}
