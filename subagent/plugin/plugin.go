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
	"github.com/gorenx/goren/session/projectioncache"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/subagent/internal/bound"
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
			pluginruntime.NewProvidedService[subagent.ExtensionRegistry](owner.extensions),
			pluginruntime.NewProvidedService[subagent.ChildDirectory](owner.directory),
			pluginruntime.NewProvidedService[subagent.BoundRegistry](owner.service),
		},
		Requires: []pluginruntime.ServiceType{
			pluginruntime.ServiceOf[agent.Registry](),
			pluginruntime.ServiceOf[agent.Constructor](),
			pluginruntime.ServiceOf[agent.RuntimeDescendants](),
			pluginruntime.ServiceOf[session.LiveStore](),
			pluginruntime.ServiceOf[persistence.Persistence](),
			pluginruntime.ServiceOf[sessionprojection.Registry](),
		},
		Optional: []pluginruntime.ServiceType{
			pluginruntime.ServiceOf[approval.DelegationPolicy](),
			pluginruntime.ServiceOf[projectioncache.Cache](),
		},
		Events: []pluginruntime.EventSubscription{
			pluginruntime.EventOf[agent.Disposed](),
			pluginruntime.EventOf[agent.SessionStarted](),
			pluginruntime.EventOf[session.EventAppended](),
		},
	}
}

// Apply resolves dependencies and activates one Subagents business object.
func (owner *Plugin) Apply(ctx context.Context) error {
	if ctx == nil {
		return errors.New("subagent: Plugin Apply context is nil")
	}
	if requestErr := ctx.Err(); requestErr != nil {
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
	projectionRegistry, err := pluginruntime.Require[sessionprojection.Registry](owner)
	if err != nil {
		return err
	}
	checkpointCache, _ := pluginruntime.Resolve[projectioncache.Cache](owner)
	commonExtensions := extensionregistry.NewProvisioner(owner.extensions)
	if err := owner.registerProjections(projectionRegistry); err != nil {
		return err
	}
	if err := owner.directory.Enable(
		liveSessions,
		sessionPersistence,
		projectionRegistry,
		checkpointCache,
	); err != nil {
		return errors.Join(
			err,
			owner.releaseProjections(context.WithoutCancel(ctx)),
		)
	}
	oneShotService, err := oneshot.New(
		oneshot.Dependencies{
			Agents:       agentRegistry,
			Constructor:  agentConstructor,
			SeedBuilders: owner.builders,
			Delegation:   approvalService,
			Extensions:   commonExtensions,
			Publisher:    owner.events,
			Executions:   owner.executions,
		},
	)
	if err != nil {
		owner.directory.Disable()
		return errors.Join(
			err,
			owner.releaseProjections(context.WithoutCancel(ctx)),
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
			Delegation:   approvalService,
			Extensions:   commonExtensions,
			Failures:     owner.failures,
			Executions:   owner.executions,
		},
	)
	if err != nil {
		owner.directory.Disable()
		return errors.Join(
			err,
			owner.releaseProjections(context.WithoutCancel(ctx)),
		)
	}
	boundService, err := bound.New(
		bound.Dependencies{
			Agents:           agentRegistry,
			Constructor:      agentConstructor,
			Sessions:         liveSessions,
			Persistence:      sessionPersistence,
			Projections:      projectionRegistry,
			SeedBuilders:     owner.builders,
			Delegation:       approvalService,
			CommonExtensions: commonExtensions,
			Extensions: boundExtensions{
				registry: owner.extensions,
			},
			Publisher:  owner.events,
			Executions: owner.executions,
			Failures:   owner.failures,
		},
	)
	if err != nil {
		owner.directory.Disable()
		return errors.Join(
			err,
			owner.releaseProjections(context.WithoutCancel(ctx)),
		)
	}
	err = owner.service.Open(
		agentRegistry,
		owner.executions,
		oneShotService,
		continuableService,
		boundService,
	)
	if err != nil {
		owner.directory.Disable()
		return errors.Join(
			err,
			owner.releaseProjections(context.WithoutCancel(ctx)),
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
