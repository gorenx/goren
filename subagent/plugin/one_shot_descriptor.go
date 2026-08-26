package plugin

import (
	"context"
	"sync"

	"github.com/gorenx/goren/agent"
	pluginruntime "github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

// descriptorAppender persists the OneShot descriptor before the first model
// step enters.
type descriptorAppender struct {
	pluginruntime.Base
	mutex      sync.Mutex
	descriptor subagent.OneShotDescriptor
	appended   bool
}

func (appender *descriptorAppender) Manifest() pluginruntime.Manifest {
	return pluginruntime.Manifest{
		Name: "@goren/subagent/one-shot-descriptor",
		Waterfalls: []pluginruntime.WaterfallMiddlewareBinding{
			pluginruntime.WaterfallOf[
				agent.PreStepNotice,
				agent.PreStepDecision,
			](appender),
		},
	}
}

func (appender *descriptorAppender) Apply(requestContext context.Context) error {
	return requestContext.Err()
}

func (*descriptorAppender) Dispose(context.Context) error {
	return nil
}

func (appender *descriptorAppender) Intercept(
	requestContext context.Context,
	notice agent.PreStepNotice,
	downstream pluginruntime.WaterfallAction[
		agent.PreStepNotice,
		agent.PreStepDecision,
	],
) (agent.PreStepDecision, error) {
	decision, decisionErr := downstream.Execute(requestContext, notice)
	if decisionErr != nil || decision.Kind != agent.PreStepEnter {
		return decision, decisionErr
	}
	appender.mutex.Lock()
	defer appender.mutex.Unlock()
	if appender.appended {
		return decision, nil
	}
	descriptorData, snapshotErr := subagent.SnapshotDescriptor(
		appender.descriptor,
	)
	if snapshotErr != nil {
		return agent.PreStepDecision{}, snapshotErr
	}
	draft, appendErr := session.NewEventDraft(
		subagent.DescriptorEvent,
		descriptorData,
	)
	if appendErr != nil {
		return agent.PreStepDecision{}, appendErr
	}
	if _, appendErr := notice.Subject.SessionValue().Commit(
		requestContext,
		session.Batch(draft),
	); appendErr != nil {
		return agent.PreStepDecision{}, appendErr
	}
	appender.appended = true
	return decision, nil
}

var _ pluginruntime.WaterfallMiddleware[
	agent.PreStepNotice,
	agent.PreStepDecision,
] = (*descriptorAppender)(nil)
