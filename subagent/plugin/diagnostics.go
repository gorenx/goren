package plugin

import (
	"fmt"

	"github.com/gorenx/goren/subagent/internal/bound"
	"github.com/gorenx/goren/subagent/internal/continuable"
)

// Diagnostics supplies process-owned failure reporting that is not Plugin
// configuration.
type Diagnostics struct {
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

func (reporter *failureReporter) ReportBoundMaterializationFailure(
	failure bound.MaterializationFailure,
) {
	reporter.report(fmt.Errorf(
		"bound subagent %q for parent %q could not materialize: %w",
		failure.ChildID,
		failure.ParentID,
		failure.Error,
	))
}

func (reporter *failureReporter) ReportBoundFinalFlushFailure(
	failure bound.FinalFlushFailure,
) {
	reporter.report(fmt.Errorf(
		"bound subagent %q best-effort final Session flush; persisted state may be stale on restore: %w",
		failure.ChildID,
		failure.Error,
	))
}

var _ continuable.FailureReporter = (*failureReporter)(nil)
var _ bound.FailureReporter = (*failureReporter)(nil)
