// Package toolbatch owns model-order progression for one Tool call batch.
package toolbatch

import (
	"errors"
	"fmt"
)

// StopReason distinguishes cancellation settlement from scheduler failure.
type StopReason uint8

const (
	// NotStopped records that dispatch has not been interrupted.
	NotStopped StopReason = iota
	// Canceled requires synthetic pairs for calls not dispatched.
	Canceled
	// ExecutionFailed forbids fabricated results after scheduler failure.
	ExecutionFailed
)

// ToolBatch owns dispatch and committed-result prefixes plus out-of-order call exits.
type ToolBatch struct {
	completed  bool
	stopReason StopReason
	limit      int
	finished   []bool
	// Both cursors advance for cancellation results of calls never dispatched.
	nextToStart            int
	nextToAccept           int
	inFlight               int
	stopsModelContinuation bool
}

// New constructs a batch ready to dispatch calls.
func New(callCount int, parallelLimit int) (*ToolBatch, error) {
	if callCount <= 0 {
		return nil, errors.New("agentloop ToolBatch: call count must be positive")
	}
	if parallelLimit <= 0 {
		return nil, errors.New("agentloop ToolBatch: parallel limit must be positive")
	}
	return &ToolBatch{
		limit:    parallelLimit,
		finished: make([]bool, callCount),
	}, nil
}

// CanStart reports whether the next model-order call can enter dispatch.
func (current *ToolBatch) CanStart() bool {
	return current != nil &&
		!current.completed && current.stopReason == NotStopped &&
		current.nextToStart < len(current.finished) &&
		current.inFlight < current.limit
}

// StartCall reserves one in-flight position before dispatch.
func (current *ToolBatch) StartCall(index int) error {
	if current == nil {
		return errors.New("agentloop ToolBatch: ToolBatch is nil")
	}
	if !current.CanStart() || index != current.nextToStart {
		return current.transitionError("start call", index)
	}
	current.nextToStart++
	current.inFlight++
	return nil
}

// CompleteCall confirms a started body or immediate policy result has finished.
func (current *ToolBatch) CompleteCall(index int) error {
	if current == nil {
		return errors.New("agentloop ToolBatch: ToolBatch is nil")
	}
	if index < current.nextToAccept || index >= current.nextToStart ||
		current.finished[index] {
		return current.transitionError("settle call", index)
	}
	if current.completed {
		return current.transitionError("settle call", index)
	}
	current.finished[index] = true
	current.inFlight--
	return nil
}

// AcceptResult records a committed real Tool result in model order.
func (current *ToolBatch) AcceptResult(
	index int,
	stopContinuation bool,
) error {
	if current == nil {
		return errors.New("agentloop ToolBatch: ToolBatch is nil")
	}
	if current.completed {
		return current.transitionError("accept call", index)
	}
	if index != current.nextToAccept || index < 0 ||
		index >= current.nextToStart ||
		!current.finished[index] {
		return current.transitionError("accept call", index)
	}
	current.nextToAccept++
	current.stopsModelContinuation = current.stopsModelContinuation ||
		stopContinuation
	return nil
}

// StopDispatch stops new calls while allowing started calls to finish.
func (current *ToolBatch) StopDispatch(reason StopReason) error {
	if current == nil {
		return errors.New("agentloop ToolBatch: ToolBatch is nil")
	}
	if current.completed || current.stopReason != NotStopped {
		return current.transitionError("stop dispatch", current.nextToStart)
	}
	if reason != Canceled && reason != ExecutionFailed {
		return errors.New("agentloop ToolBatch: drain reason is invalid")
	}
	current.stopReason = reason
	return nil
}

// AcceptCancellation records one canonical synthetic call/result pair after
// cancellation. Scheduler failure cannot use this operation.
func (current *ToolBatch) AcceptCancellation(index int) error {
	if current == nil {
		return errors.New("agentloop ToolBatch: ToolBatch is nil")
	}
	if current.completed ||
		current.stopReason != Canceled || index != current.nextToAccept ||
		index != current.nextToStart || index < 0 || index >= len(current.finished) {
		return current.transitionError("accept skipped call", index)
	}
	current.nextToStart++
	current.nextToAccept++
	return nil
}

// Complete confirms all started calls have exited and required results were
// accepted in model order. Failure leaves the batch available for completion.
func (current *ToolBatch) Complete() error {
	if current == nil {
		return errors.New("agentloop ToolBatch: ToolBatch is nil")
	}
	if current.completed {
		return current.transitionError("complete batch", current.nextToAccept)
	}
	if current.inFlight != 0 {
		return errors.New("agentloop ToolBatch: settle with in-flight calls")
	}
	if current.stopReason != ExecutionFailed &&
		current.nextToAccept != len(current.finished) {
		return errors.New("agentloop ToolBatch: settle before all results were accepted")
	}
	current.completed = true
	return nil
}

// StopsModelContinuation reports whether an accepted Tool result suppresses the
// automatic model request that would otherwise consume Tool results.
func (current *ToolBatch) StopsModelContinuation() bool {
	return current != nil && current.stopsModelContinuation
}

// IsComplete reports that batch completion has been confirmed.
func (current *ToolBatch) IsComplete() bool {
	return current != nil && current.completed
}

// StopReason returns why the batch stopped dispatching.
func (current *ToolBatch) StopReason() StopReason {
	if current == nil {
		return ExecutionFailed
	}
	return current.stopReason
}

func (current *ToolBatch) transitionError(action string, index int) error {
	return fmt.Errorf(
		"agentloop ToolBatch: cannot %s at index %d (completed %t, stop reason %d, start %d, accept %d)",
		action,
		index,
		current.completed,
		current.stopReason,
		current.nextToStart,
		current.nextToAccept,
	)
}
