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
	sources []tool.ProviderSource
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
func (bindings *Providers) Install(entries tool.Providers, sources ...tool.ProviderSource) error {
	if bindings.entries != nil {
		return errors.New("tools: Providers are already installed")
	}
	captured := make(tool.Providers, len(entries))
	for name, source := range entries {
		if name == "" ||
			strings.TrimSpace(name) != name ||
			name == tools.RunCodeName ||
			source == nil {

			return errors.New("tools: valid tool names and Providers are required")
		}
		captured[name] = source
	}
	for _, source := range sources {
		if source == nil {
			return errors.New("tools: dynamic source is nil")
		}
	}
	bindings.entries = captured
	bindings.sources = append([]tool.ProviderSource(nil), sources...)
	return nil
}

func (bindings *Providers) resolveBinding(
	name string,
	expected tools.ProviderID,
	local tool.ProviderSource,
) (tool.Provider, error) {
	var selected tool.Provider
	if installed := bindings.entries[name]; installed != nil {
		actual, err := providerIdentity(installed)
		if err != nil {
			return nil, err
		}
		if actual == expected {
			selected = installed
		}
	}
	sources := append(append([]tool.ProviderSource(nil), bindings.sources...), local)
	for _, source := range sources {
		if source == nil {
			continue
		}
		candidate, exists := source.Provider(name, expected)
		if !exists {
			continue
		}
		if candidate == nil {
			return nil, errors.New("tools: Provider source returned an empty binding")
		}
		actual, err := providerIdentity(candidate)
		if err != nil {
			return nil, err
		}
		if actual == "" || actual != expected {
			return nil, errors.New("tools: Provider changed since catalog publication")
		}
		if selected != nil {
			return nil, errors.New("tools: ambiguous tool implementation binding")
		}
		selected = candidate
	}
	if selected == nil {
		return nil, errors.New("tools: tool implementation binding is not installed")
	}
	return selected, nil
}

func providerIdentity(source tool.Provider) (tools.ProviderID, error) {
	if identified, valid := source.(tool.IdentifiedProvider); valid {
		actual := identified.ProviderID()
		if actual == "" {
			return "", errors.New("tools: replaceable Provider identity is empty")
		}
		return actual, nil
	}
	return "", nil
}

// ID identifies this ToolActor dependency in the framework extension collection.
func (*Providers) ID() string { return providersID }
