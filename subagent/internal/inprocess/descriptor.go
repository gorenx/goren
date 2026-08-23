package inprocess

import (
	"context"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

type descriptorAppender struct {
	plugin.Base
	mutex      sync.Mutex
	descriptor subagent.OneShotDescriptor
	appended   bool
}

func newDescriptorAppender(
	descriptor subagent.OneShotDescriptor,
) *descriptorAppender {
	return &descriptorAppender{
		descriptor: descriptor,
	}
}

func (appender *descriptorAppender) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "@goren/subagent/one-shot-descriptor",
		Waterfalls: []plugin.WaterfallMiddlewareBinding{
			plugin.WaterfallOf[agent.PreStepNotice, agent.PreStepDecision](appender),
		},
	}
}

func (*descriptorAppender) Apply(requestContext context.Context) error {
	return requestContext.Err()
}

func (*descriptorAppender) Dispose(context.Context) error {
	return nil
}

func (appender *descriptorAppender) Intercept(
	requestContext context.Context,
	notice agent.PreStepNotice,
	downstream plugin.WaterfallAction[agent.PreStepNotice, agent.PreStepDecision],
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
	descriptorData, snapshotErr := subagent.SnapshotDescriptor(appender.descriptor)
	if snapshotErr != nil {
		return agent.PreStepDecision{}, snapshotErr
	}
	if _, appendErr := session.AppendSerialized(
		notice.Subject.SessionValue(),
		subagent.DescriptorEvent,
		descriptorData,
	); appendErr != nil {
		return agent.PreStepDecision{}, appendErr
	}
	appender.appended = true
	return decision, nil
}

var _ plugin.WaterfallMiddleware[
	agent.PreStepNotice,
	agent.PreStepDecision,
] = (*descriptorAppender)(nil)
