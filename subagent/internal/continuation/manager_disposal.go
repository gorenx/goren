package continuation

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/subagent"
)

func (owner *Manager) dispose(
	requestContext context.Context,
	epoch *Activation,
	reason subagent.StopReason,
) error {
	transaction, opened := owner.openDisposal(epoch)
	if opened {
		return owner.finishDisposal(requestContext, epoch, transaction, reason)
	}
	select {
	case <-requestContext.Done():
		return requestContext.Err()
	case <-transaction.done:
		return transaction.err
	}
}

// openDisposal installs the synchronous admission cutoff without invoking
// Agent, Session, lifecycle, or Plugin callbacks.
func (owner *Manager) openDisposal(epoch *Activation) (*disposal, bool) {
	owner.activations.mutex.Lock()
	defer owner.activations.mutex.Unlock()
	if epoch.disposal != nil {
		return epoch.disposal, false
	}
	transaction := &disposal{
		done: make(chan struct{}),
	}
	epoch.disposal = transaction
	wake(epoch)
	return transaction, true
}

func (owner *Manager) finishDisposal(
	requestContext context.Context,
	epoch *Activation,
	transaction *disposal,
	reason subagent.StopReason,
) error {
	epoch.handle.Subject.Cancel(
		agent.ParentCancel{},
		agent.CancelOptions{
			KeepInbox: false,
		},
	)
	failures := make([]error, 0)
	if idleErr := epoch.handle.Subject.WhenIdle(requestContext); idleErr != nil {
		failures = append(failures, idleErr)
	}
	if owner.dependencies.Persistence != nil {
		if flushErr := owner.dependencies.Sessions.Flush(
			context.WithoutCancel(requestContext),
			epoch.handle.Subject.SessionValue(),
		); flushErr != nil {
			owner.dependencies.Failures.ReportFinalFlushFailure(
				FinalFlushFailure{
					ChildID: epoch.childID,
					Error:   flushErr,
				},
			)
		}
	}
	terminalReason := reason
	if reason == subagent.StopCompleted {
		terminalReason = epochStopReason(
			epoch.handle.Subject.SessionValue(),
			epoch.boundary,
			reason,
		)
	}
	lastOutput, outputErr := lastAssistant(
		epoch.handle.Subject.SessionValue(),
		epoch.boundary,
	)
	if outputErr != nil {
		failures = append(failures, outputErr)
	}
	if handleErr := epoch.handle.Dispose(
		context.WithoutCancel(requestContext),
	); handleErr != nil {
		failures = append(failures, handleErr)
	}
	transaction.err = errors.Join(failures...)
	if transaction.err != nil {
		terminalReason = subagent.StopError
		lastOutput = nil
	}
	owner.notifySettlement(epoch, lastOutput, terminalReason)

	owner.activations.mutex.Lock()
	if owner.activations.activations[epoch.childID] == epoch {
		delete(owner.activations.activations, epoch.childID)
	}
	owner.activations.mutex.Unlock()
	if epoch.parent != nil && owner.dependencies.Lifecycle != nil {
		owner.dependencies.Lifecycle.Ended(
			epoch.parent,
			subagent.Ended{
				RunID:                epoch.runID,
				Provider:             epoch.providerName,
				ID:                   epoch.childID,
				Local:                true,
				StopReason:           terminalReason,
				LastAssistantMessage: lastOutput,
			},
		)
	}
	owner.activations.mutex.Lock()
	if parentEpoch := owner.activations.activations[epoch.parentID]; parentEpoch != nil {
		wake(parentEpoch)
	}
	close(transaction.done)
	owner.activations.mutex.Unlock()
	return transaction.err
}
