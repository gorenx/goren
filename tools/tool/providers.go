package tool

import "maps"

// Providers supplies compiled tool implementations to the common ToolActor.
// Composition owns these borrowed constructors; Schema Registry never stores them.
// The key is the canonical tool name (e.g. "read_file"); the value is its
// borrowed Provider constructor, not a running tool or an Actor address.
// Each Provider.Open must still return independently owned work.
type Providers map[string]Provider

// Clone detaches name bindings while preserving borrowed constructors.
func (entries Providers) Clone() Providers { return maps.Clone(entries) }
