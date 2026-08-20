package agentloop

import (
	"context"
	"fmt"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
)

func (driver *agentDriver) emitStatus(destination agent.Status) {
	driver.mutex.Lock()
	disposed := driver.disposed
	driver.mutex.Unlock()
	if disposed {
		return
	}
	if err := plugin.Publish(
		context.Background(),
		driver.subject,
		agent.StatusChanged{
			Subject: driver.subject,
			Status:  destination,
		},
	); err != nil {
		driver.subject.owner.report(fmt.Errorf(
			"agentloop: Agent %q status observer: %w",
			driver.subject.identifier,
			err,
		))
	}
}

func (driver *agentDriver) reportError(
	requestContext context.Context,
	problem error,
) {
	driver.mutex.Lock()
	turn := driver.activity.turn
	step := driver.activity.step
	driver.mutex.Unlock()
	if requestContext == nil {
		requestContext = context.Background()
	}
	if err := plugin.Publish(
		requestContext,
		driver.subject,
		agent.AgentError{
			Subject: driver.subject,
			Turn:    turn,
			Step:    step,
			Err:     problem,
		},
	); err != nil {
		driver.subject.owner.report(fmt.Errorf(
			"agentloop: Agent %q error observer: %w",
			driver.subject.identifier,
			err,
		))
	}
}

type inboxEventBridge struct {
	subject *ReactLoopAgent
}

func (bridge inboxEventBridge) Inserted(input llm.UserMessage) {
	if err := plugin.Publish(
		context.Background(),
		bridge.subject,
		agent.InboxInserted{
			Subject: bridge.subject,
			Message: input,
		},
	); err != nil {
		bridge.subject.owner.report(err)
	}
}

func (bridge inboxEventBridge) Discarded(input llm.UserMessage) {
	if err := plugin.Publish(
		context.Background(),
		bridge.subject,
		agent.InboxDiscarded{
			Subject: bridge.subject,
			Message: input,
		},
	); err != nil {
		bridge.subject.owner.report(err)
	}
}

func (bridge inboxEventBridge) Claimed(
	input llm.UserMessage,
	turn int64,
) {
	if err := plugin.Publish(
		context.Background(),
		bridge.subject,
		agent.InboxClaimed{
			Subject: bridge.subject,
			Message: input,
			Turn:    turn,
		},
	); err != nil {
		bridge.subject.owner.report(err)
	}
}

type agentCancellation struct {
	cause agent.CancelCause
}

func (problem agentCancellation) Error() string {
	if problem.cause == nil {
		return "agent canceled"
	}
	return "agent canceled: " + problem.cause.CancelKind()
}
