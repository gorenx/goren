package execution

import (
	"errors"

	"github.com/gorenx/goren/subagent"
)

// Publish activates and registers one complete Execution, then announces its
// start to the parent Agent. The caller must finish mode-owned state linking
// before calling Publish so termination can begin immediately after exposure.
func Publish(
	registryOwner *Registry,
	lifecyclePublisher EventPublisher,
	candidate Entry,
	providerName string,
) error {
	if registryOwner == nil {
		return errors.New("subagent: Execution Registry is unavailable")
	}
	if candidate.Execution == nil {
		return errors.New("subagent: Execution publication is incomplete")
	}
	if err := candidate.Execution.Activate(); err != nil {
		return err
	}
	if err := registryOwner.Publish(candidate); err != nil {
		return err
	}
	if lifecyclePublisher != nil {
		lifecyclePublisher.PublishStarted(
			candidate.Parent,
			subagent.Started{
				RunID:    candidate.Execution.RunID(),
				Provider: providerName,
				ID:       candidate.Execution.ChildID(),
				Local:    true,
			},
		)
	}
	return nil
}
