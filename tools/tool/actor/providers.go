package toolactor

import (
	"errors"
	"strings"

	"github.com/gorenx/goren/tools"
	"github.com/gorenx/goren/tools/tool"
)

const providersID = "goren-tool-providers"

// Providers is startup-only process-local implementation wiring for ToolActors.
// Composition installs it through GoAkt WithExtensions; neither Agent Registry
// nor Tool Runtime stores or selects constructors. It owns no running work.
type Providers struct {
	// entries maps canonical tool names to borrowed Provider constructors.
	// The map is detached once; each Provider.Open must return independent work.
	entries tool.Providers
}

// NewProviders validates and detaches constructor bindings without opening work.
func NewProviders(entries tool.Providers) (*Providers, error) {
	bindings := &Providers{}
	err := bindings.Install(entries)
	if err != nil {
		return nil, err
	}
	return bindings, nil
}

// Install binds complete providers exactly once, before admitting any ToolActor.
// It is a composition-root startup operation, not a concurrent registration API.
// After success, the detached table remains immutable for the process lifetime.
func (bindings *Providers) Install(entries tool.Providers) error {
	if bindings.entries != nil {
		return errors.New("tools: Providers are already installed")
	}
	captured := make(tool.Providers, len(entries))
	for name, source := range entries {
		if name == "" || strings.TrimSpace(name) != name || name == tools.RunCodeName || source == nil {
			return errors.New("tools: valid tool names and Providers are required")
		}
		captured[name] = source
	}
	bindings.entries = captured
	return nil
}

// ID identifies this ToolActor dependency in the framework extension collection.
func (*Providers) ID() string { return providersID }
