package tokenmeter

import (
	"context"
	"errors"

	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	sessionprojection "github.com/gorenx/goren/session/projection"
)

// Plugin owns Runtime publication and teardown for one Token Meter.
type Plugin struct {
	plugin.Base

	implementation    TokenMeter
	projectionHandles []sessionprojection.UnitHandle
}

// New constructs an inactive Token Meter Plugin.
func New() *Plugin {
	return &Plugin{
		implementation: *newTokenMeter(),
	}
}

// Manifest provides the singleton Meter Service without taking model routing.
func (owner *Plugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: PluginName,
		Provides: []plugin.ProvidedService{
			plugin.NewProvidedService[Meter](&owner.implementation),
		},
		Optional: []plugin.ServiceType{
			plugin.ServiceOf[sessionprojection.Registry](),
		},
		Events: []plugin.EventSubscription{
			plugin.EventOf[session.SessionEventAppended](),
			plugin.EventOf[session.SessionDisposed](),
		},
	}
}

// Apply installs optional projection units when the shared Registry is present.
func (owner *Plugin) Apply(requestContext context.Context) error {
	if err := requestContext.Err(); err != nil {
		return err
	}
	projections, found := plugin.Resolve[sessionprojection.Registry](owner)
	if !found {
		return nil
	}
	units := []sessionprojection.Unit{
		tokenUsageUnit{},
		contextPressureUnit{},
		contextBreakdownUnit{},
	}
	acquired := make([]sessionprojection.UnitHandle, 0, len(units))
	for _, unitValue := range units {
		handle, err := projections.Register(unitValue)
		if err == nil {
			acquired = append(acquired, handle)
			continue
		}
		for handleIndex := len(acquired) - 1; handleIndex >= 0; handleIndex-- {
			err = errors.Join(err, acquired[handleIndex].Release(requestContext))
		}
		return err
	}
	owner.projectionHandles = acquired
	return nil
}

// Dispose will release optional projection effects and replay state.
func (owner *Plugin) Dispose(disposeContext context.Context) error {
	var releaseErr error
	for handleIndex := len(owner.projectionHandles) - 1; handleIndex >= 0; handleIndex-- {
		releaseErr = errors.Join(
			releaseErr,
			owner.projectionHandles[handleIndex].Release(disposeContext),
		)
	}
	owner.projectionHandles = nil
	owner.implementation.release()
	return releaseErr
}

// ObserveEvent adapts Runtime Session facts to the TokenMeter replay owner.
func (owner *Plugin) ObserveEvent(requestContext context.Context, fact plugin.Event) error {
	return owner.implementation.observeEvent(requestContext, fact)
}

var _ plugin.EventObserver = (*Plugin)(nil)
