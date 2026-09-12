package tool

import (
	"context"
	"errors"

	"github.com/gorenx/goren/tools"
)

// Execute invokes a settled implementation after Runtime policy and middleware
// admission. Concurrent calls are allowed only while configuration is immutable;
// the caller must drain all executions before any mutation or resource cleanup.
func (owner *Tool) Execute(ctx context.Context, target Revision, input tools.Invocation) tools.BodyResult {
	if ctx == nil {
		return tools.BodyResult{
			Err: errors.New("tools: execution context is required"),
		}
	}
	err := context.Cause(ctx)
	if err != nil {
		return tools.BodyResult{
			Err: err,
		}
	}
	if input.Name != owner.name {
		return tools.BodyResult{
			Err: errors.New("tools: invocation addresses a different tool"),
		}
	}
	definition, err := owner.Definition(target)
	if err != nil {
		return tools.BodyResult{
			Err: err,
		}
	}
	return input.Run(ctx, definition.Executor)
}
