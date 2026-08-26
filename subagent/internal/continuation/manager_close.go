package continuation

import (
	"context"

	"github.com/gorenx/goren/subagent"
)

// RequestClose closes business admission and starts one owned close operation
// for every resident Activation. It returns after each exact Agent has entered
// Closing, without waiting for Plugin Scope topology to be released from inside
// the current Plugin.Dispose callback.
func (owner *Manager) RequestClose(requestContext context.Context) error {
	if contextErr := checkContext(requestContext, "continuation close request"); contextErr != nil {
		return contextErr
	}
	owner.activations.mutex.Lock()
	owner.activations.admission = activationsClosing
	targets := make([]*Activation, 0, len(owner.activations.activations))
	for _, epoch := range owner.activations.activations {
		targets = append(targets, epoch)
	}
	owner.activations.mutex.Unlock()

	for _, epoch := range targets {
		selected := epoch
		go func() {
			if closeErr := owner.dispose(
				context.Background(),
				selected,
				subagent.StopAborted,
			); closeErr != nil {
				owner.dependencies.Failures.ReportCloseFailure(
					CloseFailure{
						ChildID: selected.childID,
						Error:   closeErr,
					},
				)
			}
		}()
	}
	for _, epoch := range targets {
		select {
		case <-epoch.handle.ClosingSignal():
		case <-requestContext.Done():
			return context.Cause(requestContext)
		}
	}
	return nil
}
