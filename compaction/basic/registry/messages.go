// Package compactionregistry owns Compaction instance lifetimes for Agent lifecycle orchestration.
package compactionregistry

import (
	"time"

	"github.com/gorenx/goren/compaction/basic"
	"github.com/gorenx/goren/compaction/toolresultpruner"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Name is the shared Compaction capability Registry name.
const Name = "compaction"

// Configure fixes the compiled policy and shared dependency routes before startup.
// Agent and Session routes arrive later through StartComponent.
type Configure struct {
	Config   basic.Config
	Pruner   *toolresultpruner.Config
	Commands *goakt.GrainIdentity
	Meter    *goakt.GrainIdentity
	Router   *goakt.PID
	Timeout  time.Duration
}

// Configured acknowledges validated, detached configuration.
type Configured struct{ Err error }
