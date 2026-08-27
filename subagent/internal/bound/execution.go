package bound

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	sharedexecution "github.com/gorenx/goren/subagent/internal/execution"
)

func (owner *Service) publish(
	handle agent.Handle,
	parentAgent agent.Agent,
	seedBuilder string,
	revision int64,
	currentOperation *operation,
) (*currentExecution, error) {
	runID, err := sharedexecution.NewRunID()
	if err != nil {
		return nil, err
	}
	terminator := &executionTerminator{
		owner:       owner,
		handle:      handle,
		parent:      parentAgent,
		seedBuilder: seedBuilder,
		runID:       runID,
	}
	running, err := sharedexecution.New(
		runID,
		handle.Subject.ID(),
		terminator,
	)
	if err != nil {
		return nil, err
	}
	if err = running.Activate(); err != nil {
		return nil, err
	}
	current := &currentExecution{
		running:    running,
		terminator: terminator,
		operation:  currentOperation,
		revision:   revision,
	}
	terminator.current = current
	currentOperation.storeCurrent(current)
	if err = owner.dependencies.Executions.Publish(
		sharedexecution.Entry{
			Execution: running,
			Mode:      subagent.ModeBound,
			Parent:    parentAgent,
			Subject:   handle.Subject,
			Closing:   handle.ClosingSignal(),
		},
	); err != nil {
		currentOperation.clearCurrent(current)
		return nil, err
	}
	if owner.dependencies.Publisher != nil {
		owner.dependencies.Publisher.PublishStarted(
			parentAgent,
			subagent.Started{
				RunID:    runID,
				Provider: seedBuilder,
				ID:       handle.Subject.ID(),
				Local:    true,
			},
		)
	}
	return current, nil
}

func (owner *Service) watch(current *currentExecution) {
	go func() {
		<-current.terminator.handle.ClosingSignal()
		current.running.Stop(sharedexecution.StopExternal)
	}()
}

// Interrupt cancels the current turn but retains queued Bound work and the
// resident Agent epoch.
func (owner *Service) Interrupt(
	ctx context.Context,
	childID session.SessionID,
) error {
	if err := checkContext(ctx, "Bound Interrupt"); err != nil {
		return err
	}
	current := owner.findCurrent(childID)
	if current == nil {
		return nil
	}
	if current.running.State() == subagent.ExecutionActive {
		current.terminator.handle.Subject.Cancel(
			agent.ParentCancel{},
			agent.CancelOptions{
				KeepInbox: true,
			},
		)
	}
	return nil
}

// Close stops every Bound epoch owned by this Service.
func (owner *Service) Close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	owner.mutex.Lock()
	owner.closing = true
	var workers []*interactionWorker
	for _, parentWorkers := range owner.workers {
		for _, worker := range parentWorkers {
			workers = append(workers, worker)
		}
	}
	owner.workers = nil
	operations := make([]*operation, 0, len(owner.operations))
	for _, currentOperation := range owner.operations {
		operations = append(operations, currentOperation)
	}
	owner.mutex.Unlock()
	for _, worker := range workers {
		worker.Stop()
	}
	var closeErr error
	for _, currentOperation := range operations {
		current := currentOperation.loadCurrent()
		if current == nil {
			continue
		}
		closeErr = errors.Join(
			closeErr,
			current.running.StopAndWait(ctx, sharedexecution.StopModule),
		)
	}
	return closeErr
}

func (owner *Service) replaceResidentLocked(
	ctx context.Context,
	parentAgent agent.Agent,
	childID session.SessionID,
) error {
	currentOperation := owner.childOperation(parentAgent.ID(), childID)
	current := currentOperation.loadCurrent()
	if current == nil {
		return nil
	}
	if !agent.Same(current.terminator.parent, parentAgent) {
		return unauthorizedChild(childID)
	}
	_, err := owner.startLocked(
		ctx,
		parentAgent,
		childID,
		currentOperation,
	)
	var typed *subagent.Error
	if errors.As(err, &typed) && typed.Code == subagent.ErrorBoundDisabled {
		return nil
	}
	return err
}

func (owner *Service) findCurrent(
	childID session.SessionID,
) *currentExecution {
	owner.mutex.Lock()
	operations := make([]*operation, 0, len(owner.operations))
	for key, currentOperation := range owner.operations {
		if key.childID == childID {
			operations = append(operations, currentOperation)
		}
	}
	owner.mutex.Unlock()
	for _, currentOperation := range operations {
		if current := currentOperation.loadCurrent(); current != nil {
			return current
		}
	}
	return nil
}
