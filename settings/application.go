package settings

import (
	"context"

	"github.com/gorenx/goren/mcpclient"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/tools"
)

// PromptUpdater applies the global prompt baseline without replacing local
// definitions or input already captured by an active Turn.
type PromptUpdater interface {
	ApplyPrompt(context.Context, string, systemprompt.Config) error
}

// MCPConnections owns reconciliation, publication and release of HTTP targets.
// Success means the entire target is applied. Partial failure returns an error
// and remains observable through the MCP owner's connection directory.
type MCPConnections interface {
	// Validate checks owner constraints without acquiring network resources.
	Validate(context.Context, []mcpclient.Config) error
	Reconcile(context.Context, []mcpclient.Config) error
}

// ToolUpdater checks exposed tool identities and applies complete editable flags.
// Schema, Provider bindings and execution guards remain with the Tools owner.
type ToolUpdater interface {
	ValidateTool(context.Context, string) error
	ApplyTool(context.Context, string, tools.Config) error
}
