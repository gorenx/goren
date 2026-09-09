package toolruntime

import (
	"context"
	"errors"
)

// Close resolves unfinished updates before removing the settled instances.
// The caller must drain executions first; failures retain the exact cleanup work.
func (owner *Runtime) Close(ctx context.Context, directory Catalog, instances Instances) error {
	if ctx == nil || directory == nil || instances == nil {
		return errors.New("tools: close context and ports are required")
	}
	owner.closing = true
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	if owner.pending != nil {
		// Cancellation rolls back unpublished candidates. Update preserves published
		// facts and finishes their cleanup regardless of this cancellation intention.
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		complete, err := owner.applyUpdate(canceled, directory, instances)
		if !complete {
			return err
		}
	}
	for owner.current != nil && len(owner.current.Names) != 0 {
		name := owner.current.Names[0]
		err := instances.Remove(ctx, name)
		if err != nil {
			return err
		}
		owner.current.Names = owner.current.Names[1:]
	}
	return nil
}
