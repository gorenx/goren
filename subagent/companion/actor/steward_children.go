package companionactor

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/gorenx/goren/agent"
	agentactor "github.com/gorenx/goren/agent/actor"
	agentregistry "github.com/gorenx/goren/agent/registry"
	goakt "github.com/tochemey/goakt/v4/actor"
)

type startup struct {
	cancel context.CancelFunc
}
type started struct {
	entry     *resident
	operation *startup
	ready     agentregistry.Ready
	err       error
}

type shutdown struct {
	cancel context.CancelFunc
}
type closed struct {
	entry     *resident
	operation *shutdown
	err       error
}

func (owner *steward) reconcile(gc *goakt.GrainContext, entry *resident) {
	shouldClose := owner.state == shuttingDown || entry.record.Removed || !entry.record.Enabled
	if entry.starting != nil {
		if shouldClose {
			entry.phase = Stopping
			entry.starting.cancel()
		}
		return
	}
	if entry.closing != nil {
		if owner.state == shuttingDown {
			entry.closing.cancel()
		}
		return
	}
	if entry.mutation != nil && owner.state != shuttingDown {
		return
	}
	if entry.creation.CreationID != "" {
		if shouldClose || entry.failure != nil {
			owner.closeChild(gc, entry, owner.state != shuttingDown && entry.failure == nil)
		}
		return
	}
	if owner.state == shuttingDown || entry.record.Removed || !entry.record.Enabled {
		return
	}
	if entry.failure != nil {
		return
	}
	owner.startChild(gc, entry)
}

// resume starts a new lifetime only after any previous creation has settled.
func (owner *steward) resume(gc *goakt.GrainContext, entry *resident) {
	if owner.state == active && entry.starting == nil && entry.closing == nil && entry.creation.CreationID != "" {
		owner.closeChild(gc, entry, true)
		return
	}
	owner.reconcile(gc, entry)
}

func (owner *steward) startChild(gc *goakt.GrainContext, entry *resident) {
	entry.creation = agentregistry.Creation{
		AgentID:    entry.record.ChildSessionID,
		CreationID: uuid.NewString(),
	}
	entry.phase = Starting
	ctx, cancel := context.WithCancel(context.Background())
	operation := &startup{
		cancel: cancel,
	}
	entry.starting = operation
	creator := owner.input.Creator
	parent := owner.input.Parent
	child := entry.creation
	initial := entry.record.Initial.Clone()
	err := gc.PipeToSelf(func() (any, error) {
		ready, failure := creator.Start(ctx, parent, child, initial)
		if failure == nil {
			failure = ready.Err
		}
		if failure == nil && (ready.Creation != child || ready.Executor == nil || ready.Registration.ID != child.AgentID || ready.Registration.Role != agent.CompanionRole || ready.Registration.ParentID == nil || *ready.Registration.ParentID != parent.AgentID) {
			failure = errors.New("companion: child creation returned inconsistent identity")
		}
		return started{
			entry:     entry,
			operation: operation,
			ready:     ready,
			err:       failure,
		}, nil
	})
	if err != nil {
		cancel()
		entry.starting = nil
		entry.creation = agentregistry.Creation{}
		entry.failure = err
		entry.phase = StartFailed
	}
}

func (owner *steward) started(gc *goakt.GrainContext, result started) {
	entry := result.entry
	if entry.starting != result.operation {
		return
	}
	result.operation.cancel()
	entry.starting = nil
	entry.failure = result.err
	if entry.phase == Stopping {
		if errors.Is(result.err, context.Canceled) {
			entry.failure = nil
		}
		owner.closeChild(gc, entry, false)
		owner.finishClose()
		return
	}
	if result.err == nil {
		entry.executor = result.ready.Executor
		entry.phase = Ready
	} else {
		entry.phase = StartFailed
	}
	owner.reconcile(gc, entry)
	owner.finishClose()
}

func (owner *steward) closeChild(gc *goakt.GrainContext, entry *resident, finishCurrent bool) {
	ctx, cancel := context.WithCancel(context.Background())
	operation := &shutdown{
		cancel: cancel,
	}
	entry.closing = operation
	entry.phase = Stopping
	if finishCurrent {
		entry.phase = Draining
	}
	system := gc.ActorSystem()
	authority := owner.input.Registry
	creation := entry.creation
	executor := entry.executor
	err := gc.PipeToSelf(func() (any, error) {
		var failure error
		if finishCurrent && executor != nil {
			prepared, prepareErr := ask[agentactor.Prepared](ctx, system, executor, agentactor.PrepareClose{
				CreationID:    creation.CreationID,
				FinishCurrent: true,
			})
			failure = errors.Join(prepareErr, prepared.Err)
		}
		// Parent shutdown cancels a natural-completion wait and proceeds through
		// ordinary Registry cancellation. It cannot abandon resource settlement.
		if failure == nil || ctx.Err() != nil {
			released, releaseErr := ask[agentactor.Closed](context.Background(), system, authority, agentregistry.CloseAgent{
				Creation: creation,
			})
			failure = errors.Join(releaseErr, released.Err)
			// Start has already settled: no matching reservation means this exact
			// creation owns no remaining resources. Never target a replacement.
			if errors.Is(failure, agentregistry.ErrCreation) {
				failure = nil
			}
		}
		return closed{
			entry:     entry,
			operation: operation,
			err:       failure,
		}, nil
	})
	if err != nil {
		cancel()
		entry.closing = nil
		entry.failure = err
		entry.phase = CloseFailed
	}
}

func (owner *steward) closed(gc *goakt.GrainContext, result closed) {
	entry := result.entry
	if entry.closing != result.operation {
		return
	}
	result.operation.cancel()
	entry.closing = nil
	if result.err != nil {
		entry.failure = errors.Join(entry.failure, result.err)
		entry.phase = CloseFailed
	} else {
		entry.creation = agentregistry.Creation{}
		entry.executor = nil
		entry.phase = Stopped
		if entry.failure != nil {
			entry.phase = StartFailed
		}
		if owner.state != shuttingDown && entry.failure == nil {
			owner.reconcile(gc, entry)
		}
	}
	owner.finishClose()
}

func (owner *steward) close(gc *goakt.GrainContext) {
	if owner.state == terminated {
		gc.Response(response{})
		return
	}
	reply := gc.DeferResponse()
	if reply == nil {
		gc.Response(response{
			err: errors.New("companion: close requires a request"),
		})
		return
	}
	owner.state = shuttingDown
	owner.waiters = append(owner.waiters, reply)
	for operation := range owner.deliveries {
		operation.cancel()
	}
	for _, entry := range owner.entries {
		owner.reconcile(gc, entry)
	}
	owner.finishClose()
}

func (owner *steward) finishClose() {
	if owner.state != shuttingDown || len(owner.waiters) == 0 || len(owner.deliveries) != 0 {
		return
	}
	var failures []error
	for _, entry := range owner.entries {
		if entry.starting != nil || entry.closing != nil || entry.mutation != nil && entry.mutation.stage == writeInFlight {
			return
		}
		if entry.creation.CreationID != "" {
			failure := entry.failure
			if failure == nil {
				failure = errors.New("companion: child cleanup has not settled")
			}
			failures = append(failures, failure)
		}
	}
	failure := errors.Join(failures...)
	if failure == nil {
		owner.state = terminated
	}
	for _, reply := range owner.waiters {
		reply.Response(response{
			err: failure,
		})
	}
	owner.waiters = nil
}
