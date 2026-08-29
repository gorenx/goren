package basic

import (
	"context"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/compaction"
	"github.com/gorenx/goren/compaction/toolresultpruner"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/llm/tokenmeter"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
)

// PluginName is the canonical Harness Basic Compaction Provider name.
const PluginName = "@deepseek-ai/dsh-compaction-basic"

// RuntimeOptions supplies contained automatic-recovery diagnostics.
type RuntimeOptions struct {
	ObserverError func(error)
}

// Plugin owns Runtime publication, dependency binding, hook registration, and
// teardown for one Compaction implementation.
type Plugin struct {
	plugin.Base

	catalog    *policyCatalog
	engine     *Compaction
	automation automationController
	pressure   pressureMiddleware
	overflow   overflowMiddleware
}

// New constructs an inactive Basic Compaction Provider.
func New(settings ResolvedConfig, policies RuntimeOptions) *Plugin {
	reporter := policies.ObserverError
	if reporter == nil {
		reporter = func(error) {}
	}
	catalog := newPolicyCatalog(settings)
	owner := &Plugin{
		catalog: catalog,
		engine:  newCompaction(catalog),
	}
	owner.automation = newAutomationController(
		owner.engine,
		catalog,
		reporter,
	)
	owner.pressure.controller = &owner.automation
	owner.overflow.controller = &owner.automation
	return owner
}

// Manifest provides Engine and declares every capability/hook the Provider owns.
func (owner *Plugin) Manifest() plugin.Manifest {
	waterfalls := []plugin.WaterfallMiddlewareBinding(nil)
	events := []plugin.EventSubscription(nil)
	if owner.catalog.automaticEnabled() {
		waterfalls = []plugin.WaterfallMiddlewareBinding{
			plugin.WaterfallOf(&owner.pressure),
			plugin.WaterfallOf(&owner.overflow),
		}
		events = []plugin.EventSubscription{
			plugin.EventOf[agent.StatusChanged](),
			plugin.EventOf[session.EventAppended](),
		}
	}
	return plugin.Manifest{
		Name: PluginName,
		Provides: []plugin.ProvidedService{
			plugin.NewProvidedService[compaction.Engine](owner.engine),
		},
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[llm.LlmRuntime](),
			plugin.ServiceOf[session.LiveStore](),
			plugin.ServiceOf[tokenmeter.Meter](),
		},
		Optional: []plugin.ServiceType{
			plugin.ServiceOf[toolresultpruner.Pruner](),
		},
		Events:     events,
		Waterfalls: waterfalls,
	}
}

// Apply resolves Service dependencies after Runtime admission.
func (owner *Plugin) Apply(requestContext context.Context) error {
	if err := requestContext.Err(); err != nil {
		return err
	}
	llmRuntime, err := plugin.Require[llm.LlmRuntime](owner)
	if err != nil {
		return err
	}
	sessions, err := plugin.Require[session.LiveStore](owner)
	if err != nil {
		return err
	}
	meter, err := plugin.Require[tokenmeter.Meter](owner)
	if err != nil {
		return err
	}
	pruner, _ := plugin.Resolve[toolresultpruner.Pruner](owner)
	owner.engine.bind(
		llmRuntime,
		sessions,
		meter,
		pruner,
	)
	return nil
}

// Dispose drops resolved capability snapshots after Runtime has withdrawn and drained hooks.
func (owner *Plugin) Dispose(context.Context) error {
	owner.automation.release()
	owner.engine.release()
	return nil
}

// ObserveEvent will reset overflow recovery state on success and idle boundaries.
func (owner *Plugin) ObserveEvent(requestContext context.Context, fact plugin.Event) error {
	return owner.automation.observeEvent(requestContext, fact)
}

type pressureMiddleware struct {
	controller *automationController
}

func (middleware *pressureMiddleware) Intercept(
	requestContext context.Context,
	notice agent.PreStepNotice,
	downstream plugin.WaterfallAction[agent.PreStepNotice, agent.PreStepDecision],
) (agent.PreStepDecision, error) {
	return middleware.controller.interceptPressure(
		requestContext,
		notice,
		downstream,
	)
}

type overflowMiddleware struct {
	controller *automationController
}

func (middleware *overflowMiddleware) Intercept(
	requestContext context.Context,
	notice agent.RequestErrorNotice,
	downstream plugin.WaterfallAction[agent.RequestErrorNotice, agent.RequestErrorAction],
) (agent.RequestErrorAction, error) {
	return middleware.controller.interceptOverflow(
		requestContext,
		notice,
		downstream,
	)
}

var _ plugin.EventObserver = (*Plugin)(nil)
