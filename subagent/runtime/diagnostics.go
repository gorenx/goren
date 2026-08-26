package runtime

import (
	"fmt"

	"github.com/gorenx/goren/subagent/internal/continuable"
)

// RuntimeOptions supplies process-owned hooks that are not Plugin config.
type RuntimeOptions struct {
	ObserverError func(error)
}

// failureReporter adapts process-owned diagnostic policy to Continuable's
// business failure port without making the Plugin a business capability.
type failureReporter struct {
	report func(error)
}

func (reporter *failureReporter) ReportFinalFlushFailure(
	failure continuable.FinalFlushFailure,
) {
	reporter.report(fmt.Errorf(
		"subagent %q best-effort final Session flush; persisted state may be unavailable or stale on resume: %w",
		failure.ChildID,
		failure.Error,
	))
}

var _ continuable.FailureReporter = (*failureReporter)(nil)
