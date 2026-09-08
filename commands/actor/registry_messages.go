package commandactor

import (
	"github.com/gorenx/goren/commands"
	"github.com/gorenx/goren/session"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// RegistryName is the process-wide command registry's stable Grain name.
const RegistryName = "commands"

// Register publishes a global definition or a Session override with its route.
// ID identifies the immutable publication attempt and is reused for retries.
type Register struct {
	ID         string
	SessionID  session.SessionID
	Definition commands.Definition
	Handler    *goakt.GrainIdentity
}

// Advertise supplies a default when absent and registers only this Session's
// capability address. Later global definition changes are inherited immediately.
type Advertise struct {
	ID         string
	SessionID  session.SessionID
	Definition commands.Definition
	Handler    *goakt.GrainIdentity
}

// Registered acknowledges the exact publication ID, including repeated requests.
type Registered struct {
	ID           string
	Registration commands.Registration
	Err          error
}

// Unregister retires one publication ID, including an unconfirmed attempt.
// Retirement also prevents delayed retries from resurrecting that publication.
type Unregister struct{ ID string }

// Unregistered reports whether a live publication was removed.
type Unregistered struct{ Removed bool }

// List requests the effective default and override descriptors for a Session.
type List struct{ SessionID session.SessionID }

// Descriptors is a detached, sorted metadata view.
type Descriptors struct{ Values []commands.Descriptor }

// Resolve locates an effective definition and its Session or global route.
type Resolve struct {
	SessionID session.SessionID
	Name      string
}

// Resolved contains the current effective definition and selected execution route.
type Resolved struct {
	Registration commands.Registration
	Handler      *goakt.GrainIdentity
	Found        bool
}
