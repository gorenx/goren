// Package cacheactor owns framework scheduling and I/O boundaries for projection caching.
package cacheactor

import (
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/projection"
	"github.com/gorenx/goren/session/projectioncache"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Initialize installs shared storage and an optional previously loaded record.
type Initialize struct {
	SessionID session.SessionID
	Storage   projectioncache.CheckpointStore
	Record    *projectioncache.CheckpointRecord
}

// Completion reports the actual outcome of a cache operation.
type Completion struct {
	Err error
}

// Write supplies a live checkpoint after internal orchestration confirms log durability.
type Write struct {
	Header  session.Header
	Rows    projection.Checkpoint
	Durable session.WriteBarrier
}

// Read retrieves a confirmed record belonging to the requested persistent log.
type Read struct {
	Header session.Header
}

// Record returns detached data; nil represents a cache miss.
type Record struct {
	Value *projectioncache.CheckpointRecord
	Err   error
}

// Close drains earlier admitted writes and releases this Session's Writer.
// It does not close the shared storage adapter.
type Close struct{}

// CachedSnapshot requests views of saved states using the current projection definitions.
// It does not promise the latest Session log position.
type CachedSnapshot struct {
	Header     session.Header
	Projection *goakt.GrainIdentity
}

// Snapshot returns a detached saved view; nil without an error means a cache miss.
type Snapshot struct {
	Value *projection.Snapshot
	Err   error
}

// ColdSnapshot rebuilds views from one durable log revision.
type ColdSnapshot struct {
	Storage    projectioncache.DurableEventReader
	Projection *goakt.GrainIdentity
}

// ColdView carries proven durable views and the checkpoint reconstructed with them.
type ColdView struct {
	Value *projectioncache.View
	Err   error
}

// RuntimeName returns this service's stable address within a Session lifetime.
// Business identity is carried explicitly in messages, never parsed from this name.
func RuntimeName(identifier string) string { return identifier + ".projection-cache" }
