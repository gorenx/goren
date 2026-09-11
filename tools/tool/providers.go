package tool

import (
	"context"
	"maps"

	"github.com/gorenx/goren/tools"
)

// Provider constructs an implementation from bound configuration and borrowed
// capabilities. Each Open returns independent owned resources, including any
// partially constructed resources that still require cleanup when Open fails.
type Provider interface {
	Open(context.Context) (Implementation, error)
}

// Providers supplies compiled tool implementations to the common ToolActor.
// Composition owns these borrowed constructors; Schema Registry never stores them.
// The key is the canonical tool name (e.g. "read_file"); the value is its
// borrowed Provider constructor, not a running tool or an Actor address.
// Each Provider.Open must still return independently owned work.
type Providers map[string]Provider

// ProviderSource resolves an exact published implementation without doing I/O.
// The identity comes from the authorized Session catalog, never model arguments.
// The source owns its catalog; Tool owns each implementation returned by Open.
type ProviderSource interface {
	Provider(name string, expected tools.ProviderID) (Provider, bool)
}

// IdentifiedProvider binds dynamically replaceable work to a catalog identity.
// Its ID and backing implementation must stay immutable after publication.
type IdentifiedProvider interface {
	Provider
	ProviderID() tools.ProviderID
}

// Clone detaches name bindings while preserving borrowed constructors.
func (entries Providers) Clone() Providers { return maps.Clone(entries) }
