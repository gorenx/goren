package turnrelay

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	boundcontract "github.com/gorenx/goren/subagent/bound"
)

const (
	initialRetryDelay = 250 * time.Millisecond
	maximumRetryDelay = 5 * time.Second
)

// worker owns source progress for one Binding in one exact parent Agent epoch.
type worker struct {
	owner   *Plugin
	parent  agent.Agent
	binding binding
	inbox   boundcontract.Inbox
	cursor  *sourceCursor
	ctx     context.Context
	cancel  context.CancelFunc
	wakeup  chan struct{}
}

func newWorker(
	owner *Plugin,
	workerContext context.Context,
	cancelWorker context.CancelFunc,
	parentAgent agent.Agent,
	store session.LiveStore,
	targetInbox boundcontract.Inbox,
	bindingValue binding,
) *worker {
	return &worker{
		owner:   owner,
		parent:  parentAgent,
		binding: bindingValue,
		inbox:   targetInbox,
		cursor: newSourceCursor(
			store,
			parentAgent.SessionValue(),
			bindingValue,
		),
		ctx:    workerContext,
		cancel: cancelWorker,
		wakeup: make(chan struct{}, 1),
	}
}

func (current *worker) run() {
	defer current.owner.workerClosed(current)
	defer current.cancel()
	retryDelay := initialRetryDelay
	for {
		delivered, err := current.deliverNext()
		if err != nil {
			if !current.retry(err, retryDelay) {
				return
			}
			retryDelay = min(retryDelay*2, maximumRetryDelay)
			continue
		}
		retryDelay = initialRetryDelay
		if !delivered {
			if !current.wait() {
				return
			}
		}
	}
}

func (current *worker) deliverNext() (bool, error) {
	inputValue, found, err := current.cursor.next(current.ctx)
	if err != nil || !found {
		return false, err
	}
	receiptValue, err := current.inbox.Deliver(
		current.ctx,
		current.binding.address,
		inputValue,
	)
	if err != nil {
		return false, err
	}
	if err = current.cursor.acknowledge(
		current.ctx,
		receiptValue,
	); err != nil {
		return false, err
	}
	return true, nil
}

func (current *worker) wait() bool {
	select {
	case <-current.wakeup:
		return true
	case <-current.ctx.Done():
		return false
	}
}

func (current *worker) retry(failure error, delay time.Duration) bool {
	if context.Cause(current.ctx) != nil {
		return false
	}
	var problem *subagent.Error
	if !errors.As(failure, &problem) ||
		problem.Code != subagent.ErrorBoundDisabled {
		current.owner.report(fmt.Errorf(
			"subagent/bound/turnrelay: relay user Session %q Bound %q: %w",
			current.binding.address.SessionID,
			current.binding.address.Name,
			failure,
		))
	}
	retryTimer := time.NewTimer(delay)
	defer retryTimer.Stop()
	select {
	case <-retryTimer.C:
		return true
	case <-current.wakeup:
		return true
	case <-current.ctx.Done():
		return false
	}
}

func (current *worker) wake() {
	select {
	case current.wakeup <- struct{}{}:
	default:
	}
}
