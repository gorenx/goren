// Package plugin implements the Subagent Plugin and composes its business
// objects into the host Plugin Runtime.
package plugin

import (
	"context"
	"errors"
	"fmt"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/approval"
	pluginruntime "github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/session/persistence"
	sessionprojection "github.com/gorenx/goren/session/projection"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/subagent/internal/childdirectory"
	"github.com/gorenx/goren/subagent/internal/continuable"
	sharedexecution "github.com/gorenx/goren/subagent/internal/execution"
	extensionregistry "github.com/gorenx/goren/subagent/internal/extension"
	"github.com/gorenx/goren/subagent/internal/oneshot"
	"github.com/gorenx/goren/subagent/internal/seedbuilder"
	"github.com/gorenx/goren/subagent/internal/subagents"
)

// Plugin owns only dependency resolution, capability publication, and the
// active Subagents business object's structural lifetime.
type Plugin struct {
	pluginruntime.Base

	builders    *seedbuilder.Registry
	service     *subagents.Service
	executions  *sharedexecution.Registry
	extensions  *extensionregistry.Registry
	directory   *childdirectory.Service
	events      *eventPublisher
	projections []sessionprojection.UnitHandle
	failures    *failureReporter
}

// New constructs an inactive Plugin and its stable publication adapters.
func New(failureReporting Diagnostics) *Plugin {
	reporter := failureReporting.ObserverError
	if reporter == nil {
		reporter = func(error) {}
	}
	owner := &Plugin{
		service:    subagents.New(),
		executions: sharedexecution.NewRegistry(),
		extensions: extensionregistry.New(),
		directory:  childdirectory.New(),
		failures: &failureReporter{
			report: reporter,
		},
	}
	owner.events = &eventPublisher{
		owner: owner,
	}
	owner.builders = seedbuilder.New(owner.events)
	return owner
}

// Manifest publishes narrow business capability views and declares every
// dependency required to run both Subagent implementations.
func (owner *Plugin) Manifest() pluginruntime.Manifest {
	return pluginruntime.Manifest{
		Name: subagent.PluginName,
		Provides: []pluginruntime.ProvidedService{
			pluginruntime.NewProvidedService[subagent.SeedBuilderRegistry](owner.builders),
			pluginruntime.NewProvidedService[subagent.Starter](owner.service),
			pluginruntime.NewProvidedService[subagent.ChildControl](owner.service),
			pluginruntime.NewProvidedService[subagent.ParentReporter](owner.service),
			pluginruntime.NewProvidedService[subagent.ExtensionRegistry](owner.extensions),
			pluginruntime.NewProvidedService[subagent.ChildDirectory](owner.directory),
		},
		Requires: []pluginruntime.ServiceType{
			pluginruntime.ServiceOf[agent.Registry](),
			pluginruntime.ServiceOf[agent.Constructor](),
			pluginruntime.ServiceOf[agent.RuntimeDescendants](),
			pluginruntime.ServiceOf[session.LiveStore](),
			pluginruntime.ServiceOf[persistence.Persistence](),
		},
		Optional: []pluginruntime.ServiceType{
			pluginruntime.ServiceOf[approval.DelegationPolicy](),
			pluginruntime.ServiceOf[sessionprojection.Registry](),
		},
		Events: []pluginruntime.EventSubscription{
			pluginruntime.EventOf[agent.Disposed](),
		},
	}
}

// Apply resolves dependencies and activates one Subagents business object.
func (owner *Plugin) Apply(requestContext context.Context) error {
	if requestContext == nil {
		return errors.New("subagent: Plugin Apply context is nil")
	}
	if requestErr := requestContext.Err(); requestErr != nil {
		return requestErr
	}
	agentRegistry, err := pluginruntime.Require[agent.Registry](owner)
	if err != nil {
		return err
	}
	agentConstructor, err := pluginruntime.Require[agent.Constructor](owner)
	if err != nil {
		return err
	}
	runtimeDescendants, err := pluginruntime.Require[agent.RuntimeDescendants](owner)
	if err != nil {
		return err
	}
	liveSessions, err := pluginruntime.Require[session.LiveStore](owner)
	if err != nil {
		return err
	}
	sessionPersistence, err := pluginruntime.Require[persistence.Persistence](owner)
	if err != nil {
		return err
	}
	approvalService, _ := pluginruntime.Resolve[approval.DelegationPolicy](owner)
	projectionRegistry, _ := pluginruntime.Resolve[sessionprojection.Registry](owner)
	environments := &environmentBuilder{
		delegation: approvalService,
		extensions: owner.extensions,
	}
	if err := owner.registerProjections(projectionRegistry); err != nil {
		return err
	}
	if err := owner.directory.Enable(
		liveSessions,
		sessionPersistence,
		projectionRegistry,
	); err != nil {
		return errors.Join(
			err,
			owner.releaseProjections(context.WithoutCancel(requestContext)),
		)
	}
	oneShotService, err := oneshot.New(
		oneshot.Dependencies{
			Agents:       agentRegistry,
			Constructor:  agentConstructor,
			SeedBuilders: owner.builders,
			Environments: environments,
			Publisher:    owner.events,
			Executions:   owner.executions,
		},
	)
	if err != nil {
		owner.directory.Disable()
		return errors.Join(
			err,
			owner.releaseProjections(context.WithoutCancel(requestContext)),
		)
	}
	continuableService, err := continuable.New(
		continuable.Dependencies{
			Agents:       agentRegistry,
			Constructor:  agentConstructor,
			Descendants:  runtimeDescendants,
			Sessions:     liveSessions,
			Persistence:  sessionPersistence,
			SeedBuilders: owner.builders,
			Publisher:    owner.events,
			Environments: environments,
			Failures:     owner.failures,
			Executions:   owner.executions,
		},
	)
	if err != nil {
		owner.directory.Disable()
		return errors.Join(
			err,
			owner.releaseProjections(context.WithoutCancel(requestContext)),
		)
	}
	err = owner.service.Open(
		agentRegistry,
		owner.executions,
		oneShotService,
		continuableService,
	)
	if err != nil {
		owner.directory.Disable()
		return errors.Join(
			err,
			owner.releaseProjections(context.WithoutCancel(requestContext)),
		)
	}
	return nil
}

// Dispose withdraws admission, converges live Executions, then releases
// registration and projection resources owned by this Plugin cycle.
func (owner *Plugin) Dispose(closeContext context.Context) error {
	if closeContext == nil {
		closeContext = context.Background()
	}
	completionContext := context.WithoutCancel(closeContext)
	serviceErr := owner.service.Close(completionContext)
	owner.directory.Disable()
	danglingExtensions, extensionErr := owner.extensions.Clear(completionContext)
	if danglingExtensions != 0 {
		extensionErr = errors.Join(
			extensionErr,
			fmt.Errorf(
				"subagent: Plugin stopped with %d registered Extension(s)",
				danglingExtensions,
			),
		)
	}
	danglingBuilders := owner.builders.Clear()
	projectionErr := owner.releaseProjections(completionContext)
	if danglingBuilders == 0 {
		return errors.Join(serviceErr, extensionErr, projectionErr)
	}
	return errors.Join(
		serviceErr,
		extensionErr,
		projectionErr,
		fmt.Errorf(
			"subagent: Plugin stopped with %d registered SeedBuilder(s)",
			danglingBuilders,
		),
	)
}

var _ pluginruntime.Plugin = (*Plugin)(nil)
