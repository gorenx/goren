package plugin

// runtimeEntry is the private publication protocol implemented by typed
// Service bindings, Middleware bindings, and Observer subscriptions. Each
// runtimeEntry is represented by one Fiber-owned effect.
type runtimeEntry interface {
	Label() string
	diagnostic() runtimeEntryDiagnostic
	validateEntry(ownership *fiberEffect) error
	publishEntry(ownership *fiberEffect)
	withdrawEntry(ownership *fiberEffect)
}

type runtimeEntryKind uint8

const (
	runtimeEntryService runtimeEntryKind = iota
	runtimeEntryWaterfall
	runtimeEntryEvent
)

type runtimeEntryDiagnostic struct {
	kind runtimeEntryKind
	name string
}
