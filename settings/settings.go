package settings

import (
	"context"
	"github.com/gorenx/goren/subagent/companion"
	"github.com/gorenx/goren/tools"
)

// Settings is the business capability offered to configuration consumers.
// It describes provenance and conditionally edits complete user overrides.
// Revisions survive reset so stale editors cannot recreate an obsolete value.
// A successful update confirms persistence; consumers select saved values at
// their execution boundary. Cancellation cannot undo an already committed save.
type Settings interface {
	UpdateAgent(context.Context, string, int64, Values) (Snapshot, error)
	UpdateCompanion(context.Context, string, int64, companion.Config) (Snapshot, error)
	ResetCompanion(context.Context, string, int64) (Snapshot, error)
	Describe(context.Context) (Snapshot, error)
	UpdateTool(context.Context, string, int64, tools.Config) (Snapshot, error)
	ResetTool(context.Context, string, int64) (Snapshot, error)
	UpdateProvider(context.Context, string, int64, ProviderConfig) (Snapshot, error)
	UpdatePrompt(context.Context, string, int64, PromptConfig) (Snapshot, error)
	UpdateMCP(context.Context, string, int64, MCPConfig) (Snapshot, error)
	ResetProvider(context.Context, string, int64) (Snapshot, error)
	ResetPrompt(context.Context, string, int64) (Snapshot, error)
	ResetMCP(context.Context, string, int64) (Snapshot, error)
}
