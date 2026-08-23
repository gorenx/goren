package continuation

import "github.com/gorenx/goren/session"

// FinalFlushFailure identifies a contained best-effort durability failure.
// Activation release still completes because retaining the child would pin its
// complete ancestor chain.
type FinalFlushFailure struct {
	ChildID session.SessionID
	Error   error
}

// FailureReporter receives failures that cannot be returned to the caller
// without changing Subagent lifecycle semantics.
type FailureReporter interface {
	ReportFinalFlushFailure(FinalFlushFailure)
}
