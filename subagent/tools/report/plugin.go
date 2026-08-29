// Package report installs the child-scoped Subagent report Tool.
package report

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/subagent"
)

const PluginName = "@deepseek-ai/dsh-tool-subagent-report"

// Delivery controls how an accepted report schedules the direct parent Agent.
type Delivery string

const (
	// Quiet appends without waking an idle parent.
	Quiet Delivery = "quiet"
	// NextStep schedules the report for the parent's next model step.
	NextStep Delivery = "next-step"
)

// Plugin registers one report Extension for child Agents.
type Plugin struct {
	plugin.Base
	scheduling   Delivery
	tool         *reportTool
	registration subagent.ExtensionRegistration
}

// New validates the report scheduling policy.
func New(selectedDelivery Delivery) (*Plugin, error) {
	switch selectedDelivery {
	case Quiet, NextStep:
	default:
		return nil, errors.New("subagent report: unsupported reportDelivery")
	}
	return &Plugin{
		scheduling: selectedDelivery,
	}, nil
}

// Manifest publishes the private Tool provider inherited by child Scopes and
// declares the Extension and live Agent dependencies.
func (owner *Plugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: PluginName,
		Provides: []plugin.ProvidedService{
			plugin.NewProvidedService[reportToolProvider](owner),
		},
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[subagent.ExtensionRegistry](),
			plugin.ServiceOf[agent.Registry](),
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
	agents, requireErr := plugin.Require[agent.Registry](owner)
	if requireErr != nil {
		return requireErr
	}
	reporting, constructionErr := newReportTool(agents, owner.scheduling)
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

// Dispose unregisters the Extension and releases its resident installations.
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
