package toolruntime

import (
	"context"
	"errors"
)

// Close drains unfinished replacement work and then stops each retained tool.
// Errors preserve the exact remaining resource responsibilities for another attempt.
func (owner *Toolset) Close(ctx context.Context, instances Instances) error {
	if ctx == nil || instances == nil {
		return errors.New("tools: close context and instances are required")
	}
	owner.closing = true
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	if owner.pending != nil {
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		complete, err := owner.pending.Apply(cancelled, instances)
		if !complete {
			return err
		}
		if err == nil {
			owner.current = cloneTools(owner.pending.tools)
		}
		owner.pending = nil
	}
	for len(owner.current) != 0 {
		err := instances.Remove(ctx, owner.current[0].Schema.Name)
		if err != nil {
			return err
		}
		owner.current = owner.current[1:]
	}
	return nil
}
