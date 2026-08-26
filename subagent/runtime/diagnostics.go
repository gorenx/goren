package runtime

import (
	"fmt"

	"github.com/gorenx/goren/subagent/internal/continuation"
)

// RuntimeOptions supplies process-owned hooks that are not Plugin config.
type RuntimeOptions struct {
	ObserverError func(error)
}

// failureReporter adapts process-owned diagnostic policy to continuation's
// business failure port without making the Plugin a business capability.
type failureReporter struct {
	report func(error)
}

func (reporter *failureReporter) ReportFinalFlushFailure(
	failure continuation.FinalFlushFailure,
) {
	reporter.report(fmt.Errorf(
		"subagent %q best-effort final Session flush; persisted state may be unavailable or stale on resume: %w",
		failure.ChildID,
		failure.Error,
	))
}

// ReportCloseFailure reports structural close completion that runs after the
// Subagent Plugin has already withdrawn its business capabilities.
func (reporter *failureReporter) ReportCloseFailure(
	failure continuation.CloseFailure,
) {
	reporter.report(fmt.Errorf(
		"subagent %q managed Agent close failed after Subagent Plugin disposal: %w",
		failure.ChildID,
		failure.Error,
	))
}

var _ continuation.FailureReporter = (*failureReporter)(nil)
