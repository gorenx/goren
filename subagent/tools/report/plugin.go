// Package report installs the child-scoped continuable Subagent report Tool.
package report

import (
	"context"
	"errors"

	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/subagent"
)

const PluginName = "@deepseek-ai/dsh-tool-subagent-report"

// Plugin registers one child-scoped Extension for Continuable children.
type Plugin struct {
	plugin.Base
	delivery     subagent.ReportDelivery
	tool         *reportTool
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

// Manifest publishes the private reporter dependency that ties resident child
// Plugins to this host Plugin, then declares the Extension and delivery
// use-case Services.
func (owner *Plugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: PluginName,
		Provides: []plugin.ProvidedService{
			plugin.NewProvidedService[reportToolProvider](owner),
		},
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[subagent.ExtensionRegistry](),
			plugin.ServiceOf[subagent.ParentReporter](),
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
	reports, requireErr := plugin.Require[subagent.ParentReporter](owner)
	if requireErr != nil {
		return requireErr
	}
	reporting, constructionErr := newReportTool(reports, owner.delivery)
	if constructionErr != nil {
		return constructionErr
	}
	owner.tool = reporting
	registration, registerErr := extensions.RegisterExtension(&extension{})
	if registerErr != nil {
		owner.tool = nil
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
	owner.tool = nil
	return unregisterErr
}

type reportToolProvider interface {
	Tool() *reportTool
}

func (owner *Plugin) Tool() *reportTool {
	return owner.tool
}

var _ reportToolProvider = (*Plugin)(nil)
