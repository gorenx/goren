package commands

import (
	"context"

	"github.com/gorenx/goren/plugin"
)

// Plugin owns Runtime publication and teardown for one CommandRuntime.
type Plugin struct {
	plugin.Base
	implementation *CommandRuntime
}

// New constructs an inactive Commands Plugin.
func New(options RuntimeOptions) (*Plugin, error) {
	implementation, err := NewCommandRuntime(options)
	if err != nil {
		return nil, err
	}
	return &Plugin{
		implementation: implementation,
	}, nil
}

// Manifest publishes the Commands registry Service.
func (owner *Plugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: PluginName,
		Provides: []plugin.ProvidedService{
			plugin.NewProvidedService[Registry](owner.implementation),
		},
	}
}

// Apply validates the activation Context before publication.
func (*Plugin) Apply(requestContext context.Context) error {
	return requestContext.Err()
}

// Dispose withdraws and drains every registration left by a failed or partial
// consumer teardown.
func (owner *Plugin) Dispose(closeContext context.Context) error {
	return owner.implementation.Close(closeContext)
}
