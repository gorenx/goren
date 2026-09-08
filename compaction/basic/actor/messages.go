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
