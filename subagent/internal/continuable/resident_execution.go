package continuable

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/gorenx/goren/agent"
	agentregistry "github.com/gorenx/goren/agent/registry"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/subagent/internal/agentclient"
	sharedexecution "github.com/gorenx/goren/subagent/internal/execution"
)

type residentExecution struct {
	mutex       sync.RWMutex
	phase       subagent.ExecutionState
	done        chan struct{}
	terminal    subagent.Terminal
	terminalErr error
	owner       *Service
	runID       subagent.RunID
	child       agentclient.Agent
	parent      agentregistry.Creation
	provider    string
	boundary    int64
}

func newResidentExecution(owner *Service, runID subagent.RunID, child agentclient.Agent, parent agentregistry.Creation, provider string, boundary int64) *residentExecution {
	return &residentExecution{phase: subagent.ExecutionStarting, done: make(chan struct{}), owner: owner, runID: runID, child: child, parent: parent, provider: provider, boundary: boundary}
}
func (resident *residentExecution) Activate() error {
	resident.mutex.Lock()
	defer resident.mutex.Unlock()
	if resident.phase != subagent.ExecutionStarting {
		return errors.New("subagent: Continuable execution is no longer starting")
	}
	resident.phase = subagent.ExecutionActive
	return nil
}
func (resident *residentExecution) RunID() subagent.RunID      { return resident.runID }
func (resident *residentExecution) ChildID() session.SessionID { return resident.child.ID() }
func (resident *residentExecution) State() subagent.ExecutionState {
	resident.mutex.RLock()
	defer resident.mutex.RUnlock()
	return resident.phase
}
func (resident *residentExecution) Stop(cause sharedexecution.CloseCause) {
	resident.stop(context.Background(), cause)
}
func (resident *residentExecution) stop(ctx context.Context, cause sharedexecution.CloseCause) {
	resident.mutex.Lock()
	if resident.phase == subagent.ExecutionStopping || resident.phase == subagent.ExecutionStopped {
		resident.mutex.Unlock()
		return
	}
	resident.phase = subagent.ExecutionStopping
	resident.mutex.Unlock()
	go resident.terminate(context.WithoutCancel(ctx), cause)
}
func (resident *residentExecution) watch() {
	_ = resident.child.WhenIdle(context.Background())
	resident.Stop(sharedexecution.CloseIdle)
}
func (resident *residentExecution) terminate(ctx context.Context, cause sharedexecution.CloseCause) {
	terminal, err := resident.close(ctx, cause)
	resident.mutex.Lock()
	resident.terminal = sharedexecution.CloneTerminal(terminal)
	resident.terminalErr = err
	resident.phase = subagent.ExecutionStopped
	close(resident.done)
	resident.mutex.Unlock()
}
func (resident *residentExecution) close(ctx context.Context, cause sharedexecution.CloseCause) (subagent.Terminal, error) {
	var closeErr error
	if cause != sharedexecution.CloseIdle && cause != sharedexecution.CloseNormal {
		closeErr = errors.Join(closeErr, resident.child.Cancel(ctx, agent.ParentCancel{}, agent.CancelOptions{KeepInbox: false}))
	}
	closeErr = errors.Join(closeErr, resident.child.WhenIdle(ctx))
	view, readErr := resident.child.Read(ctx)
	closeErr = errors.Join(closeErr, readErr)
	fallback := subagent.StopAborted
	if cause == sharedexecution.CloseIdle || cause == sharedexecution.CloseNormal {
		fallback = subagent.StopCompleted
	}
	terminal := subagent.Terminal{StopReason: fallback}
	if readErr == nil {
		events, eventErr := currentExecutionEvents(view.Snapshot.Events, resident.boundary)
		closeErr = errors.Join(closeErr, eventErr)
		terminal.StopReason = executionStopReason(events, fallback)
		terminal.Output, eventErr = sharedexecution.SelectAssistantOutput(events)
		closeErr = errors.Join(closeErr, eventErr)
	}
	if closeErr != nil {
		terminal.StopReason = subagent.StopError
		terminal.Output = nil
	}
	resident.notifyParent(ctx, terminal)
	if resident.owner.dependencies.Publisher != nil {
		resident.owner.dependencies.Publisher.PublishEnded(resident.parent, subagent.Ended{RunID: resident.runID, Provider: resident.provider, ID: resident.child.ID(), Local: true, StopReason: terminal.StopReason, LastAssistantMessage: terminal.Output})
	}
	resident.owner.dependencies.Executions.Remove(resident)
	resident.owner.remove(resident)
	if cause != sharedexecution.CloseExternal {
		if err := resident.child.Close(ctx); err != nil {
			resident.owner.dependencies.Failures.ReportFinalFlushFailure(FinalFlushFailure{ChildID: resident.child.ID(), Error: err})
			closeErr = errors.Join(closeErr, err)
		}
	}
	return terminal, closeErr
}
func (resident *residentExecution) notifyParent(ctx context.Context, terminal subagent.Terminal) {
	parent, err := resident.owner.dependencies.Agents.Find(ctx, resident.parent.AgentID)
	if err != nil {
		return
	}
	summary := settlementSummary(resident.child.ID(), terminal.StopReason)
	content := []agentmessage.ContentBlock{agentmessage.NewTextBlock(summary)}
	if len(terminal.Output) == 0 {
		content = append(content, agentmessage.NewTextBlock("It left no closing message."))
	} else {
		content = append(content, agentmessage.NewTextBlock("Its closing message:"))
		content = append(content, terminal.Output...)
	}
	message, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{Content: content, Source: subagent.SettlementSource{Summary: summary, SenderSessionID: resident.child.ID()}})
	if err != nil {
		return
	}
	activity, err := parent.Activity(ctx)
	if err == nil && activity.Status == agent.StatusRunning {
		_ = parent.Steer(ctx, message)
	} else {
		_ = parent.Followup(ctx, message)
	}
}
func (resident *residentExecution) Wait(ctx context.Context) error {
	if ctx == nil {
		return errors.New("subagent: Continuable Wait context is nil")
	}
	select {
	case <-resident.done:
		resident.mutex.RLock()
		defer resident.mutex.RUnlock()
		return resident.terminalErr
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}
func (resident *residentExecution) Result() (subagent.Terminal, bool) {
	resident.mutex.RLock()
	defer resident.mutex.RUnlock()
	if resident.phase != subagent.ExecutionStopped {
		return subagent.Terminal{}, false
	}
	return sharedexecution.CloneTerminal(resident.terminal), true
}
func (resident *residentExecution) Dispose(ctx context.Context) error {
	return resident.StopAndWait(ctx, sharedexecution.CloseDisposed)
}
func (resident *residentExecution) StopAndWait(ctx context.Context, cause sharedexecution.CloseCause) error {
	if ctx == nil {
		ctx = context.Background()
	}
	resident.stop(ctx, cause)
	return resident.Wait(ctx)
}

func currentExecutionEvents(events []session.Event, boundary int64) ([]session.Event, error) {
	if boundary < 0 || boundary > int64(len(events)) {
		return nil, fmt.Errorf("subagent: invalid Continuable execution boundary %d", boundary)
	}
	return append([]session.Event(nil), events[boundary:]...), nil
}
func executionStopReason(events []session.Event, fallback subagent.StopReason) subagent.StopReason {
	work, err := agent.FoldConsumedWork(events)
	if err != nil {
		return subagent.StopError
	}
	if work.End == nil {
		if work.DroppedUnrun {
			return subagent.StopAborted
		}
		return fallback
	}
	switch work.End.Reason.TurnEndKind() {
	case "completed":
		if work.DroppedUnrun {
			return subagent.StopAborted
		}
		return subagent.StopCompleted
	case "blocked":
		return subagent.StopRefusal
	case "max-tokens":
		return subagent.StopMaxTokens
	case "interrupted", "aborted":
		return subagent.StopAborted
	default:
		return subagent.StopError
	}
}
func settlementSummary(childID session.SessionID, reason subagent.StopReason) string {
	subject := fmt.Sprintf("Background subagent %s", childID)
	switch reason {
	case subagent.StopCompleted:
		return subject + " finished and can be resumed by another message."
	case subagent.StopAborted:
		return subject + " was stopped before it finished."
	case subagent.StopMaxTokens:
		return subject + " ran out of room before it finished."
	case subagent.StopRefusal:
		return subject + " declined the task."
	default:
		return subject + " failed before it finished."
	}
}

var _ sharedexecution.ManagedExecution = (*residentExecution)(nil)
