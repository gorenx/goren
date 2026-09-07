// Package titleactor owns asynchronous Title requests and Session commit ordering.
package titleactor

import (
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/title"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// Initialize binds validated title policy to one Session creation.
type Initialize struct {
	// CreationID is an orchestration identity, never a persisted title field.
	CreationID string
	Session    *goakt.GrainIdentity
	Config     title.Config
	Header     session.Header
	Provider   title.Provider
}

// Read requests the title from the currently committed Session log.
type Read struct {
	CreationID string
}

// View contains detached title data, or nil before any title exists.
type View struct {
	Title *title.Snapshot
	Err   error
}

// Rename requests an explicit user title and pins automatic generation.
type Rename struct {
	CreationID string
	Title      string
}

// EnsureFallback fills an absent title from the first human message.
type EnsureFallback struct {
	CreationID string
}

// Updated acknowledges a Session memory commit, not persistence flush.
// Empty Events mean the policy found no change to make.
type Updated struct {
	Result session.WriteResult
	Err    error
}

// Close releases policy after preceding Session requests have completed.
type Close struct {
	CreationID string
}

// Completion acknowledges initialization or closure.
type Completion struct {
	Err error
}

// Generate requests a model title from a committed Session snapshot.
// Provider is selected by composition; automatic subscription supplies the
// registered provider rather than exposing it through an external protocol.
type Generate struct {
	CreationID string
	Provider   title.Provider
	// Refresh permits replacing the user title observed when this request starts.
	// With no Provider or no human messages, refresh uses the deterministic fallback policy.
	Refresh bool
}
