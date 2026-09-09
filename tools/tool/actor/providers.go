package toolactor

import (
	"errors"
	"strings"

	"github.com/gorenx/goren/tools"
	"github.com/gorenx/goren/tools/tool"
)

const providersID = "goren-tool-providers"

// Providers is immutable process-local implementation wiring for ToolActors.
// Composition installs it through GoAkt WithExtensions; neither Agent Registry
// nor Tool Runtime stores or selects constructors. It owns no running work.
type Providers struct {
	// entries maps canonical tool names to borrowed Provider constructors.
	// The map is detached once; each Provider.Open must return independent work.
	entries tool.Providers
}

// NewProviders validates and detaches constructor bindings without opening work.
func NewProviders(entries tool.Providers) (*Providers, error) {
	captured := entries.Clone()
	for name, source := range captured {
		if name == "" || strings.TrimSpace(name) != name || name == tools.RunCodeName || source == nil {
			return nil, errors.New("tools: valid tool names and Providers are required")
		}
	}
	return &Providers{entries: captured}, nil
}

// ID identifies this ToolActor dependency in the framework extension collection.
func (*Providers) ID() string { return providersID }
