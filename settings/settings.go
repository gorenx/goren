package settings

import (
	"context"
	"github.com/gorenx/goren/subagent/companion"
	"github.com/gorenx/goren/tools"
)

// Settings is the business capability offered to configuration consumers.
// It describes provenance, conditionally edits user overrides, and retries a
// saved target's application. Implementations may call Service directly or
// communicate with its Actor; callers do not depend on messages or storage.
//
// All updates replace complete named groups; reset removes the complete override.
// Revisions survive reset so stale editors cannot recreate an obsolete value.
//
// An update may return both a Snapshot and ErrApplication: the target is saved,
// but its owner has not accepted the whole change. Cancellation after commit
// does not undo the saved target; Describe allows the caller to reconcile it.
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
	RetryApplication(context.Context, Group, int64) (Snapshot, error)
}
