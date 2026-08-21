package assembly

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/gorenx/goren/plugin"
	pluginfactory "github.com/gorenx/goren/plugin/factory"
)

const serverPluginName = "@gorenx/goren/server"

type boundEndpoint interface {
	BoundAddress() string
}

// Server is the complete detached Plugin tree constructed by assembly before
// Runtime admission. It owns topology only; child Plugins own all behavior.
type Server struct {
	plugin.Base
	children []plugin.ChildPlugin
	endpoint boundEndpoint
}

// BuildServer constructs every declared Plugin through its owning Factory and
// returns one complete tree without mounting or performing effects.
func BuildServer(
	createContext context.Context,
	directory *pluginfactory.Catalog,
	specs []PluginSpec,
) (*Server, error) {
	if createContext == nil {
		return nil, errors.New("assembly: create Context is nil")
	}
	if directory == nil {
		return nil, errors.New("assembly: Factory Catalog is nil")
	}
	if len(specs) == 0 {
		return nil, errors.New("assembly: Server requires at least one Plugin")
	}
	assembled := &Server{
		children: make([]plugin.ChildPlugin, 0, len(specs)),
	}
	for index, spec := range specs {
		if err := createContext.Err(); err != nil {
			return nil, err
		}
		if strings.TrimSpace(spec.FactoryName) == "" ||
			spec.FactoryName != strings.TrimSpace(spec.FactoryName) {
			return nil, fmt.Errorf(
				"assembly: Plugin declaration %d has an invalid Factory name",
				index,
			)
		}
		if spec.Phase != plugin.ActivationMain &&
			spec.Phase != plugin.ActivationCommit {
			return nil, fmt.Errorf(
				"assembly: Plugin declaration %q has an invalid activation phase",
				spec.FactoryName,
			)
		}
		builder, err := directory.Lookup(spec.FactoryName)
		if err != nil {
			return nil, fmt.Errorf(
				"assembly: select Factory %q: %w",
				spec.FactoryName,
				err,
			)
		}
		instance, err := builder.Create(
			createContext,
			append([]byte(nil), spec.Config...),
		)
		if err != nil {
			return nil, fmt.Errorf(
				"assembly: create Plugin %q: %w",
				spec.FactoryName,
				err,
			)
		}
		if instance == nil {
			return nil, fmt.Errorf(
				"assembly: Factory %q returned a nil Plugin",
				spec.FactoryName,
			)
		}
		pluginName := instance.Manifest().Name
		if pluginName != spec.FactoryName {
			return nil, fmt.Errorf(
				"assembly: Factory %q returned Plugin %q",
				spec.FactoryName,
				pluginName,
			)
		}
		if endpoint, matches := instance.(boundEndpoint); matches {
			if assembled.endpoint != nil {
				return nil, errors.New(
					"assembly: Server declares more than one bound endpoint",
				)
			}
			assembled.endpoint = endpoint
		}
		assembled.children = append(
			assembled.children,
			plugin.ChildPlugin{
				Instance:  instance,
				Placement: plugin.SameScope,
				Phase:     spec.Phase,
			},
		)
	}
	return assembled, nil
}

// Manifest declares the complete same-Scope Server topology.
func (owner *Server) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name:     serverPluginName,
		Children: append([]plugin.ChildPlugin(nil), owner.children...),
	}
}

// Apply has no behavior; Runtime activates child owners from the declaration.
func (*Server) Apply(requestContext context.Context) error {
	return requestContext.Err()
}

// Dispose has no behavior because Runtime stops every owned child first.
func (*Server) Dispose(context.Context) error {
	return nil
}

// BoundAddress returns the Connection endpoint after successful Runtime start.
func (owner *Server) BoundAddress() string {
	if owner == nil || owner.endpoint == nil {
		return ""
	}
	return owner.endpoint.BoundAddress()
}
