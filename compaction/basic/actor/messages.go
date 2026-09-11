// Package compactionactor schedules Compaction business operations through GoAkt.
package compactionactor

import (
	"time"

	"github.com/gorenx/goren/compaction/basic"
	"github.com/gorenx/goren/compaction/toolresultpruner"
	"github.com/gorenx/goren/session"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Initialize binds one Agent's compiled business policy and dependency routes.
type Initialize struct {
	// Commands is the already-started global Registry supplied by composition.
	Commands *goakt.GrainIdentity
	// Agent is the existing AgentLoop maintenance route; command registration starts automatically.
	Agent      *goakt.GrainIdentity
	AgentID    session.SessionID
	Session    *goakt.GrainIdentity
	CreationID string
	Meter      *goakt.GrainIdentity
	Router     *goakt.PID
	Config     basic.Config
	Pruner     *toolresultpruner.Config
	Timeout    time.Duration
}

// Completion acknowledges initialization or a safe shutdown of the binding.
type Completion struct{ Err error }

// Execute starts one exclusive invocation and replies only after settlement.
type Execute struct {
	// Agent is the maintenance admission route required by manual execution.
	Agent *goakt.GrainIdentity
	// RequestID correlates manual cancellation with this invocation only.
	RequestID       string
	Mode            basic.Mode
	Fallback        basic.RouteTarget
	SourceCommandID *string
}

// Result carries a settled business outcome, including a settled failure.
// Intermediate business facts are committed to Session instead of replying here.
type Result struct {
	Outcome basic.Outcome
	Err     error
}

// Close releases an idle binding; active transactions must settle first.
type Close struct{}

// Cancel signals the matching manual invocation without settling Execute.
// Late cancellation for a different invocation is an acknowledged no-op.
type Cancel struct {
	RequestID string
	Cause     error
}

// RuntimeName returns this service's stable address within a Session lifetime.
// Business identity is carried explicitly in messages, never parsed from this name.
func RuntimeName(identifier string) string { return identifier + ".compaction" }
