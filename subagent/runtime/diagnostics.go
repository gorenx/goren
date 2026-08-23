package runtime

import (
	"fmt"

	"github.com/gorenx/goren/subagent/internal/continuation"
)

// RuntimeOptions supplies process-owned hooks that are not Plugin config.
type RuntimeOptions struct {
	ObserverError func(error)
}

func (owner *Plugin) ReportFinalFlushFailure(
	failure continuation.FinalFlushFailure,
) {
	owner.report(fmt.Errorf(
		"subagent %q best-effort final Session flush; persisted state may be unavailable or stale on resume: %w",
		failure.ChildID,
		failure.Error,
	))
}

var _ continuation.FailureReporter = (*Plugin)(nil)
