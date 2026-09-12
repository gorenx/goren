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

// MCPUpdater validates and publishes the complete HTTP connection configuration.
// Success confirms catalog acceptance, not network connectivity in every Session.
type MCPUpdater interface {
	ValidateMCP(context.Context, []mcpclient.Config) error
	ApplyMCP(context.Context, []mcpclient.Config) error
}

// ToolUpdater checks exposed tool identities and applies complete editable flags.
// Schema, Provider bindings and execution guards remain with the Tools owner.
type ToolUpdater interface {
	ValidateTool(context.Context, string) error
	ApplyTool(context.Context, string, tools.Config) error
}
