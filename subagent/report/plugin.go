// Package report installs the child-scoped continuable Subagent report Tool.
package report

import (
	"context"
	"errors"

	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/subagent"
)

const PluginName = "@deepseek-ai/dsh-tool-subagent-report"

// Plugin registers one Activation Extension for continuable children.
type Plugin struct {
	plugin.Base
	delivery     subagent.ReportDelivery
	registration subagent.ExtensionRegistration
}

// New validates the report scheduling policy.
func New(delivery subagent.ReportDelivery) (*Plugin, error) {
	switch delivery {
	case subagent.ReportQuiet, subagent.ReportNextStep:
	default:
		return nil, errors.New("subagent report: unsupported reportDelivery")
	}
	return &Plugin{
		delivery: delivery,
	}, nil
}

// Manifest declares only the Extension and delivery use-case Services.
func (*Plugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: PluginName,
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[subagent.ExtensionRegistry](),
			plugin.ServiceOf[subagent.ContinuableService](),
		},
	}
}

// Apply registers the child contribution.
func (owner *Plugin) Apply(requestContext context.Context) error {
	if requestContext == nil {
		return errors.New("subagent report: Apply context is nil")
	}
	if requestErr := requestContext.Err(); requestErr != nil {
		return requestErr
	}
	extensions, requireErr := plugin.Require[subagent.ExtensionRegistry](owner)
	if requireErr != nil {
		return requireErr
	}
	continuations, requireErr := plugin.Require[subagent.ContinuableService](owner)
	if requireErr != nil {
		return requireErr
	}
	registration, registerErr := extensions.RegisterExtension(
		&extension{
			continuations: continuations,
			delivery:      owner.delivery,
		},
	)
	if registerErr != nil {
		return registerErr
	}
	owner.registration = registration
	return nil
}

// Dispose unregisters the Extension and revokes its resident installations.
func (owner *Plugin) Dispose(closeContext context.Context) error {
	if owner.registration == nil {
		return nil
	}
	unregisterErr := owner.registration.Unregister(closeContext)
	owner.registration = nil
	return unregisterErr
}
