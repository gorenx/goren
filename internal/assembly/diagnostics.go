package assembly

import (
	"context"
	"errors"
	"fmt"

	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/persistence"
	"github.com/gorenx/goren/session/projectioncache"
	"github.com/gorenx/goren/session/title"
)

// Diagnostics adapts contained failures from domain-specific reporting ports
// to one process-owned sink without moving failure policy into a domain.
type Diagnostics struct {
	report func(error)
}

// NewDiagnostics constructs the required process failure adapter.
func NewDiagnostics(reportFailure func(error)) (*Diagnostics, error) {
	if reportFailure == nil {
		return nil, errors.New("assembly: failure reporter is required")
	}
	return &Diagnostics{
		report: reportFailure,
	}, nil
}

// Report forwards one already contextualized contained failure.
func (owner *Diagnostics) Report(problem error) {
	if owner == nil || problem == nil {
		return
	}
	owner.report(problem)
}

// ReportEventFailure adapts Runtime best-effort Event delivery failure.
func (owner *Diagnostics) ReportEventFailure(
	_ context.Context,
	failure plugin.EventFailure,
) {
	owner.Report(fmt.Errorf(
		"runtime Event %q delivery: %w",
		failure.EventName,
		failure.Error,
	))
}

// ReportObserverFailure adapts contained LLM topology observer failure.
func (owner *Diagnostics) ReportObserverFailure(problem error) {
	owner.Report(fmt.Errorf("LLM topology observer: %w", problem))
}

// ReportPostCommitFailure adapts Session work that failed after commit.
func (owner *Diagnostics) ReportPostCommitFailure(
	failure session.PostCommitFailure,
) {
	owner.Report(fmt.Errorf(
		"Session %q post-commit work: %w",
		failure.SessionID,
		failure.Error,
	))
}

// ReportBackgroundWriteFailure adapts asynchronous Session durability failure.
func (owner *Diagnostics) ReportBackgroundWriteFailure(
	failure persistence.BackgroundWriteFailure,
) {
	owner.Report(fmt.Errorf(
		"Session %q background write through %s: %w",
		failure.SessionID,
		failure.BackendName,
		failure.Error,
	))
}

// ReportProjectionCacheFailure adapts contained checkpoint cache work.
func (owner *Diagnostics) ReportProjectionCacheFailure(
	failure projectioncache.Failure,
) {
	owner.Report(fmt.Errorf(
		"Session %q projection cache %s: %w",
		failure.SessionID,
		failure.Operation,
		failure.Error,
	))
}

// ReportAsyncFailure adapts contained Session title work failure.
func (owner *Diagnostics) ReportAsyncFailure(failure title.AsyncFailure) {
	owner.Report(fmt.Errorf(
		"Session %q asynchronous title work: %w",
		failure.SessionID,
		failure.Error,
	))
}
