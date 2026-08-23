package inprocess

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

type settlement struct {
	result subagent.Result
	err    error
}

// Run owns one exact published local child until Dispose completes.
type Run struct {
	id         session.SessionID
	child      agent.Agent
	handle     agent.Handle
	boundary   int64
	structured *structuredCapture
	cancelled  atomic.Bool
	settled    chan struct{}
	settlement settlement
	dispose    sync.Once
	disposed   chan struct{}
	disposeErr error
}

func newRun(
	requestContext context.Context,
	handle agent.Handle,
	prompt llm.UserMessage,
	boundary int64,
	structured *structuredCapture,
) *Run {
	owned := &Run{
		id:         handle.Subject.ID(),
		child:      handle.Subject,
		handle:     handle,
		boundary:   boundary,
		structured: structured,
		settled:    make(chan struct{}),
		disposed:   make(chan struct{}),
	}
	go owned.watchCancellation(requestContext)
	go owned.drive(prompt)
	return owned
}

// ID returns the durable child Session identity.
func (owned *Run) ID() session.SessionID {
	if owned == nil {
		return ""
	}
	return owned.id
}

// LocalAgent returns the exact same-process child.
func (owned *Run) LocalAgent() (agent.Agent, bool) {
	if owned == nil || owned.child == nil {
		return nil, false
	}
	return owned.child, true
}

// AwaitResult waits without changing the underlying run lifecycle.
func (owned *Run) AwaitResult(
	requestContext context.Context,
) (subagent.Result, error) {
	if owned == nil {
		return subagent.Result{}, errors.New("subagent: one-shot Run is nil")
	}
	if requestContext == nil {
		return subagent.Result{}, errors.New("subagent: AwaitResult context is nil")
	}
	select {
	case <-requestContext.Done():
		return subagent.Result{}, requestContext.Err()
	case <-owned.settled:
		return cloneResult(owned.settlement.result), owned.settlement.err
	}
}

// Dispose cancels unfinished work and releases the exact Agent Handle after
// both execution and teardown have settled.
func (owned *Run) Dispose(closeContext context.Context) error {
	if owned == nil {
		return nil
	}
	if closeContext == nil {
		closeContext = context.Background()
	}
	owned.dispose.Do(func() {
		go func() {
			owned.cancelled.Store(true)
			owned.child.Cancel(
				agent.DisposedCancel{},
				agent.CancelOptions{
					KeepInbox: false,
				},
			)
			disposeErr := owned.handle.Dispose(context.Background())
			<-owned.settled
			owned.disposeErr = disposeErr
			close(owned.disposed)
		}()
	})
	select {
	case <-closeContext.Done():
		return closeContext.Err()
	case <-owned.disposed:
		return owned.disposeErr
	}
}

func (owned *Run) watchCancellation(requestContext context.Context) {
	select {
	case <-owned.settled:
		return
	case <-requestContext.Done():
		owned.cancelled.Store(true)
		owned.child.Cancel(
			agent.ParentCancel{},
			agent.CancelOptions{
				KeepInbox: false,
			},
		)
	}
}

func (owned *Run) drive(prompt llm.UserMessage) {
	if !owned.cancelled.Load() {
		if followErr := owned.child.Followup(prompt); followErr != nil {
			owned.settlement.err = followErr
			close(owned.settled)
			return
		}
	}
	if idleErr := owned.child.WhenIdle(context.Background()); idleErr != nil {
		owned.settlement.err = idleErr
		close(owned.settled)
		return
	}
	owned.settlement.result, owned.settlement.err = readResult(
		owned.child.SessionValue(),
		owned.boundary,
		owned.cancelled.Load(),
		owned.structured,
	)
	close(owned.settled)
}

func cloneResult(source subagent.Result) subagent.Result {
	detached := source
	detached.Output, _ = llm.CloneContentBlocks(source.Output)
	detached.Structured = append([]byte(nil), source.Structured...)
	if source.Diagnostic != nil {
		diagnosticValue := *source.Diagnostic
		detached.Diagnostic = &diagnosticValue
	}
	return detached
}

var _ subagent.Run = (*Run)(nil)
