package tool

import (
	"context"
	"errors"
)

// Provision constructs and validates one replacement. Every returned resource
// is either retained as a candidate or released, even when construction fails.
// The caller serializes this entire operation with every other Tool method.
func (owner *Tool) Provision(ctx context.Context, target Revision, source Provider) error {
	if err := owner.checkPreparation(target); err != nil {
		return err
	}
	if ctx == nil || source == nil {
		return errors.New("tools: preparation requires context and provider")
	}
	if err := ctx.Err(); err != nil {
		return context.Cause(ctx)
	}
	input, err := source.Open(ctx)
	if err == nil {
		err = context.Cause(ctx)
	}
	if err == nil {
		err = owner.acceptImplementation(target, input)
	}
	if err == nil {
		return nil
	}
	if cleanup := closeResource(context.WithoutCancel(ctx), input.Resource); cleanup != nil {
		// The failed implementation is retained solely for cleanup. It must not
		// become visible to catalog registration or executable after a retry.
		owner.candidate = &candidate{
			revision:         target,
			body:             input,
			discardRequested: true,
		}
		return errors.Join(err, cleanup)
	}
	return err
}
