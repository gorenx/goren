// Package runtime composes Subagent business objects into the Plugin Runtime.
package runtime

import (
	"context"
	"errors"
	"fmt"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/approval"
	"github.com/gorenx/goren/plugin"
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
	plugin.Base

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
func New(options RuntimeOptions) *Plugin {
	reporter := options.ObserverError
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
func (owner *Plugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: subagent.PluginName,
		Provides: []plugin.ProvidedService{
			plugin.NewProvidedService[subagent.SeedBuilderRegistry](owner.builders),
			plugin.NewProvidedService[subagent.Starter](owner.service),
			plugin.NewProvidedService[subagent.ChildControl](owner.service),
			plugin.NewProvidedService[subagent.ParentReporter](owner.service),
			plugin.NewProvidedService[subagent.ExtensionRegistry](owner.extensions),
			plugin.NewProvidedService[subagent.ChildDirectory](owner.directory),
		},
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[agent.Registry](),
			plugin.ServiceOf[agent.Constructor](),
			plugin.ServiceOf[agent.RuntimeDescendants](),
			plugin.ServiceOf[session.LiveStore](),
			plugin.ServiceOf[persistence.Persistence](),
		},
		Optional: []plugin.ServiceType{
			plugin.ServiceOf[approval.DelegationPolicy](),
			plugin.ServiceOf[sessionprojection.Registry](),
		},
		Events: []plugin.EventSubscription{
			plugin.EventOf[agent.Disposed](),
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
	agentRegistry, err := plugin.Require[agent.Registry](owner)
	if err != nil {
		return err
	}
	agentConstructor, err := plugin.Require[agent.Constructor](owner)
	if err != nil {
		return err
	}
	runtimeDescendants, err := plugin.Require[agent.RuntimeDescendants](owner)
	if err != nil {
		return err
	}
	liveSessions, err := plugin.Require[session.LiveStore](owner)
	if err != nil {
		return err
	}
	sessionPersistence, err := plugin.Require[persistence.Persistence](owner)
	if err != nil {
		return err
	}
	approvalService, _ := plugin.Resolve[approval.DelegationPolicy](owner)
	projectionRegistry, _ := plugin.Resolve[sessionprojection.Registry](owner)
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
			Approval:     approvalService,
			SeedBuilders: owner.builders,
			Lifecycle:    owner.events,
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
			Approval:     approvalService,
			SeedBuilders: owner.builders,
			Lifecycle:    owner.events,
			Extensions:   owner.extensions,
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
		continuableService,
		continuableService,
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

var _ plugin.Plugin = (*Plugin)(nil)
