package settings

import (
	"context"
	"errors"
)

// RetryApplication applies a saved named target without changing its revision.
// A canceled waiter cannot undo a committed setting.
func (owner *Service) RetryApplication(ctx context.Context, identifier Group, expected int64) (Snapshot, error) {
	if ctx == nil || !identifier.Valid() {
		return owner.Snapshot(), ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return owner.Snapshot(), err
	}
	if expected < 0 || owner.state.Version(identifier) != expected {
		return owner.Snapshot(), ErrConflict
	}
	if !owner.target.Has(identifier) && expected == 0 {
		return owner.Snapshot(), ErrInvalid
	}
	application := owner.applications[identifier]
	if identifier.Section == ProviderSection {
		application.PendingRestart = expected != application.AppliedRevision
		owner.applications[identifier] = application
		return owner.Snapshot(), nil
	}
	var err error
	switch identifier.Section {
	case CompanionSection, AgentSection:
	// The directory is already installed; existing instances require explicit Apply.
	case ToolSection:
		err = owner.toolsets.ApplyTool(ctx, identifier.Name, owner.target.Tools[identifier.Name].Clone())
	case PromptSection:
		err = owner.prompts.ApplyPrompt(ctx, identifier.Name, owner.target.Prompts[identifier.Name].Prompt())
	case MCPSection:
		err = owner.connections.Reconcile(ctx, owner.target.MCPConnections())
	default:
		return owner.Snapshot(), ErrInvalid
	}
	if err == nil {
		owner.applications[identifier] = Application{
			AppliedRevision: expected,
		}
	} else {
		application.Error = ErrApplication.Error()
		owner.applications[identifier] = application
	}
	// MCP reconciliation confirms the complete captured set, including removals.
	if identifier.Section == MCPSection {
		for key, revision := range owner.state.Versions {
			if key.Section != MCPSection {
				continue
			}
			current := owner.applications[key]
			if err == nil {
				current = Application{
					AppliedRevision: revision,
				}
			} else if current.AppliedRevision != revision {
				current.Error = ErrApplication.Error()
			}
			owner.applications[key] = current
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return owner.Snapshot(), err
	}
	if err != nil {
		return owner.Snapshot(), ErrApplication
	}
	return owner.Snapshot(), nil
}
