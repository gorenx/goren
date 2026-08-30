package oneshot

import (
	"context"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

// descriptorAppender persists the OneShot descriptor before the first model
// step enters.
type descriptorAppender struct {
	mutex      sync.Mutex
	descriptor subagent.OneShotDescriptor
	appended   bool
}

func (appender *descriptorAppender) InterceptPreStep(
	ctx context.Context,
	notice agent.PreStepNotice,
	downstream agent.PreStepAction,
) (agent.PreStepDecision, error) {
	decision, decisionErr := downstream.Execute(ctx, notice)
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
		ctx,
		session.Batch(draft),
	); appendErr != nil {
		return agent.PreStepDecision{}, appendErr
	}
	appender.appended = true
	return decision, nil
}

var _ agent.PreStepMiddleware = (*descriptorAppender)(nil)
