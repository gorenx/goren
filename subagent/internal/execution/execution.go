// Package execution owns the state, current-run index, identities, and final
// assistant-output rule shared by OneShot and Continuable implementations.
package execution

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

// EventPublisher publishes the paired facts emitted by both Subagent
// implementations.
type EventPublisher interface {
	PublishStarted(agent.Agent, subagent.Started)
	PublishEnded(agent.Agent, subagent.Ended)
}

// StopCause identifies why an implementation requested the common terminal
// transaction. It is internal input, not a caller-visible terminal result.
type StopCause string

const (
	StopNormal      StopCause = "normal-result"
	StopIdle        StopCause = "idle-settlement"
	StopInterrupted StopCause = "interrupt"
	StopDisposed    StopCause = "dispose"
	StopModule      StopCause = "module-shutdown"
	StopExternal    StopCause = "external-agent-close"
)

// Terminator completes the mode-specific physical and durable work for one
// already-claimed terminal transaction.
type Terminator interface {
	Terminate(context.Context, StopCause) (subagent.Terminal, error)
}

type terminalResult struct {
	value subagent.Terminal
	err   error
}

// Execution is the concrete shared state owner for one exact Subagent epoch.
type Execution struct {
	mutex      sync.RWMutex
	runID      subagent.RunID
	childID    session.SessionID
	state      subagent.ExecutionState
	terminator Terminator
	done       chan struct{}
	terminal   terminalResult
}

// New constructs one unpublished starting Execution.
func New(
	lifecycleID subagent.RunID,
	childSessionID session.SessionID,
	terminalHandler Terminator,
) (*Execution, error) {
	if lifecycleID == "" {
		return nil, errors.New("subagent: Execution RunID is empty")
	}
	if childSessionID == "" {
		return nil, errors.New("subagent: Execution child ID is empty")
	}
	if terminalHandler == nil {
		return nil, errors.New("subagent: Execution Terminator is required")
	}
	return &Execution{
		runID:      lifecycleID,
		childID:    childSessionID,
		state:      subagent.ExecutionStarting,
		terminator: terminalHandler,
		done:       make(chan struct{}),
	}, nil
}

// Activate commits initial Inbox acceptance and makes the Execution visible.
func (running *Execution) Activate() error {
	if running == nil {
		return errors.New("subagent: Execution is nil")
	}
	running.mutex.Lock()
	defer running.mutex.Unlock()
	if running.state != subagent.ExecutionStarting {
		return errors.New("subagent: Execution is no longer starting")
	}
	running.state = subagent.ExecutionActive
	return nil
}

// Stop claims or joins the one terminal transaction.
func (running *Execution) Stop(cause StopCause) {
	if running == nil {
		return
	}
	running.mutex.Lock()
	if running.state == subagent.ExecutionStopping ||
		running.state == subagent.ExecutionStopped {
		running.mutex.Unlock()
		return
	}
	running.state = subagent.ExecutionStopping
	running.mutex.Unlock()
	go running.terminate(cause)
}

func (running *Execution) terminate(cause StopCause) {
	terminalValue, terminalErr := running.terminator.Terminate(
		context.Background(),
		cause,
	)
	running.mutex.Lock()
	running.terminal = terminalResult{
		value: cloneTerminal(terminalValue),
		err:   terminalErr,
	}
	running.state = subagent.ExecutionStopped
	close(running.done)
	running.mutex.Unlock()
}

// RunID returns the lifecycle-event correlation identity.
func (running *Execution) RunID() subagent.RunID {
	if running == nil {
		return ""
	}
	return running.runID
}

// ChildID returns the durable child Session identity.
func (running *Execution) ChildID() session.SessionID {
	if running == nil {
		return ""
	}
	return running.childID
}

// State returns the current common execution phase.
func (running *Execution) State() subagent.ExecutionState {
	if running == nil {
		return subagent.ExecutionStopped
	}
	running.mutex.RLock()
	currentState := running.state
	running.mutex.RUnlock()
	return currentState
}

// Wait waits for the terminal transaction without changing execution state.
func (running *Execution) Wait(requestContext context.Context) error {
	if running == nil {
		return errors.New("subagent: Execution is nil")
	}
	if requestContext == nil {
		return errors.New("subagent: Execution Wait context is nil")
	}
	select {
	case <-running.done:
		return running.terminationError()
	default:
	}
	select {
	case <-requestContext.Done():
		return requestContext.Err()
	case <-running.done:
		return running.terminationError()
	}
}

func (running *Execution) terminationError() error {
	running.mutex.RLock()
	defer running.mutex.RUnlock()
	return running.terminal.err
}

// Result returns the immutable terminal result after the Execution stops.
// It never waits and never changes lifecycle state.
func (running *Execution) Result() (subagent.Terminal, bool) {
	if running == nil {
		return subagent.Terminal{}, false
	}
	running.mutex.RLock()
	defer running.mutex.RUnlock()
	if running.state != subagent.ExecutionStopped {
		return subagent.Terminal{}, false
	}
	return cloneTerminal(running.terminal.value), true
}

// Dispose requests early termination or joins the existing terminal work.
func (running *Execution) Dispose(closeContext context.Context) error {
	return running.StopAndWait(closeContext, StopDisposed)
}

// StopAndWait requests one internal stop cause or joins the stop already in
// progress. Implementations use it when an external Agent lifecycle event must
// complete Subagent settlement synchronously.
func (running *Execution) StopAndWait(
	closeContext context.Context,
	cause StopCause,
) error {
	if running == nil {
		return nil
	}
	if closeContext == nil {
		closeContext = context.Background()
	}
	running.Stop(cause)
	return running.Wait(closeContext)
}

func cloneTerminal(source subagent.Terminal) subagent.Terminal {
	detached := source
	detached.Output, _ = agentmessage.CloneContentBlocks(source.Output)
	detached.Structured = append(json.RawMessage(nil), source.Structured...)
	if source.Diagnostic != nil {
		diagnosticValue := *source.Diagnostic
		detached.Diagnostic = &diagnosticValue
	}
	return detached
}

var _ subagent.Execution = (*Execution)(nil)
