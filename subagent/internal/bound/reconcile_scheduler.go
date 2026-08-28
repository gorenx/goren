package bound

import (
	"context"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
)

type reconcileTask struct {
	subject   agent.Agent
	requested bool
	running   bool
}

// reconcileScheduler owns every SessionStarted and Definition-change task.
// A parent has at most one running task while different parents remain
// independent.
type reconcileScheduler struct {
	owner  *Service
	ctx    context.Context
	cancel context.CancelFunc
	mutex  sync.Mutex
	// Key is a user Session ID. Value is its latest exact parent Agent and
	// level-triggered reconciliation state. Failed work remains dormant here
	// until the next lifecycle, Definition, or interaction trigger.
	parents   map[session.SessionID]*reconcileTask
	tasks     sync.WaitGroup
	closing   bool
	closeOnce sync.Once
	closed    chan struct{}
}

func newReconcileScheduler(
	requestContext context.Context,
	owner *Service,
) *reconcileScheduler {
	lifecycleContext, cancelLifecycle := context.WithCancel(
		context.WithoutCancel(requestContext),
	)
	return &reconcileScheduler{
		owner:   owner,
		ctx:     lifecycleContext,
		cancel:  cancelLifecycle,
		parents: make(map[session.SessionID]*reconcileTask),
		closed:  make(chan struct{}),
	}
}

func (scheduler *reconcileScheduler) request(parentAgent agent.Agent) {
	if !isUserAgent(parentAgent) {
		return
	}
	parentID := parentAgent.ID()
	scheduler.mutex.Lock()
	if scheduler.closing {
		scheduler.mutex.Unlock()
		return
	}
	current := scheduler.parents[parentID]
	if current != nil {
		current.subject = parentAgent
		current.requested = true
		if current.running {
			scheduler.mutex.Unlock()
			return
		}
		scheduler.startLocked(current)
		scheduler.mutex.Unlock()
		go scheduler.run(parentID, current)
		return
	}
	task := &reconcileTask{
		subject:   parentAgent,
		requested: true,
	}
	scheduler.parents[parentID] = task
	scheduler.startLocked(task)
	scheduler.mutex.Unlock()
	go scheduler.run(parentID, task)
}

// retry reactivates only a retained failed task. A normal turn/end must not
// create new reconciliation work after the parent is already aligned.
func (scheduler *reconcileScheduler) retry(parentAgent agent.Agent) {
	if !isUserAgent(parentAgent) {
		return
	}
	parentID := parentAgent.ID()
	scheduler.mutex.Lock()
	current := scheduler.parents[parentID]
	if scheduler.closing || current == nil || current.running {
		scheduler.mutex.Unlock()
		return
	}
	current.subject = parentAgent
	current.requested = true
	scheduler.startLocked(current)
	scheduler.mutex.Unlock()
	go scheduler.run(parentID, current)
}

func (scheduler *reconcileScheduler) startLocked(task *reconcileTask) {
	task.running = true
	scheduler.tasks.Add(1)
}

func (scheduler *reconcileScheduler) DefinitionsChanged() {
	if scheduler == nil || scheduler.owner == nil ||
		scheduler.owner.dependencies.Agents == nil {
		return
	}
	for _, parentAgent := range scheduler.owner.dependencies.Agents.List() {
		scheduler.request(parentAgent)
	}
}

func (scheduler *reconcileScheduler) agentDisposed(subject agent.Agent) {
	if subject == nil {
		return
	}
	scheduler.mutex.Lock()
	task := scheduler.parents[subject.ID()]
	if task != nil && agent.Same(task.subject, subject) {
		task.subject = nil
		task.requested = false
		if !task.running {
			delete(scheduler.parents, subject.ID())
		}
	}
	scheduler.mutex.Unlock()
}

func (scheduler *reconcileScheduler) run(
	parentID session.SessionID,
	task *reconcileTask,
) {
	defer scheduler.tasks.Done()
	for {
		scheduler.mutex.Lock()
		if scheduler.closing {
			delete(scheduler.parents, parentID)
			scheduler.mutex.Unlock()
			return
		}
		parentAgent := task.subject
		task.requested = false
		scheduler.mutex.Unlock()
		var reconcileErr error
		if parentAgent != nil && scheduler.owner.dependencies.Agents != nil &&
			scheduler.owner.dependencies.Agents.Contains(parentAgent) {
			reconcileErr = scheduler.owner.reconcileParent(
				scheduler.ctx,
				parentAgent,
			)
		}
		if reconcileErr != nil && context.Cause(scheduler.ctx) == nil {
			scheduler.owner.reportReconcileFailure(parentID, reconcileErr)
		}
		scheduler.mutex.Lock()
		if scheduler.closing {
			if scheduler.parents[parentID] == task {
				delete(scheduler.parents, parentID)
			}
			task.running = false
			scheduler.mutex.Unlock()
			return
		}
		if task.requested {
			scheduler.mutex.Unlock()
			continue
		}
		task.running = false
		if (reconcileErr == nil || task.subject == nil) &&
			scheduler.parents[parentID] == task {
			delete(scheduler.parents, parentID)
		}
		scheduler.mutex.Unlock()
		return
	}
}

func (scheduler *reconcileScheduler) close(
	closeContext context.Context,
) error {
	scheduler.closeOnce.Do(func() {
		scheduler.mutex.Lock()
		scheduler.closing = true
		scheduler.parents = nil
		scheduler.mutex.Unlock()
		scheduler.cancel()
		go func() {
			scheduler.tasks.Wait()
			close(scheduler.closed)
		}()
	})
	select {
	case <-scheduler.closed:
		return nil
	case <-closeContext.Done():
		return context.Cause(closeContext)
	}
}

var _ DefinitionReconciler = (*reconcileScheduler)(nil)
