package registry

import (
	"github.com/gorenx/goren/session"
	sessionactor "github.com/gorenx/goren/session/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// lifecycle groups all framework-facing work and results for one Session creation.
// RegistryGrain owns it and accesses it only on its mailbox turns. Reservation
// and activation authorization remain owned by the private creation authority.
type lifecycle struct {
	// creationID identifies the reserved business creation, not the stable SessionID.
	// Initialization, close, and retries use this value to reject stale requests.
	// A new reservation replaces this whole record rather than changing its ID.
	creationID string

	// header is present only after successful initialization. It is immutable
	// creation metadata for listing, not a copy of the mutable Session log.
	header *session.Header

	// endpoint addresses the initialized Session Grain. Resolution does not activate it.
	endpoint *goakt.GrainIdentity

	// initialization fixes the original input and storage port and tracks callers
	// waiting for initialization. It remains after success or failure so retries
	// use the same input; non-nil does not mean initialization is still running.
	// Its settled channel signals when the current attempt ends. Close can wait
	// for that signal while retaining its own replies. Successful close clears it.
	initialization *initialization

	// closure holds the accepted close operation, its waiting callers, and the
	// completed business-close checkpoint used when framework deactivation retries.
	// It also blocks new initialization requests. It can coexist with initialization
	// while waiting for that attempt to finish. Failure retains it; success clears it.
	closure *closure

	// result is set only after business close, Grain deactivation, and reservation
	// release all succeed. Repeated close requests for creationID receive this result
	// without repeating those effects. When set, initialization and closure are nil.
	// A new creation replaces the record and discards this previous close result.
	result *sessionactor.Closed
}
