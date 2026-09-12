package settings

import (
	"context"
	"github.com/gorenx/goren/mcpclient"
)

// MCPValidator checks connection definitions against installed capability names.
// Validation performs no publication or connection mutation.
type MCPValidator interface {
	ValidateMCP(context.Context, []mcpclient.Config) error
}

// ToolValidator checks that a tool is exposed for configuration.
// Schemas and execution guards remain owned by the Tools registry.
type ToolValidator interface {
	ValidateTool(context.Context, string) error
}
