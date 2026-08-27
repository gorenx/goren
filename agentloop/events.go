package agentloop

import (
	"context"
	"fmt"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
)

// observerFailureReporter contains failures that occur after a live fact has
// committed and can no longer be rolled back.
type observerFailureReporter struct {
	reportOperation func(error)
}

func newObserverFailureReporter(
	reportOperation func(error),
) observerFailureReporter {
	if reportOperation == nil {
		reportOperation = func(error) {}
	}
	return observerFailureReporter{
		reportOperation: reportOperation,
	}
}

func (reporter observerFailureReporter) report(problem error) {
	if problem == nil {
		return
	}
	defer func() {
		_ = recover()
	}()
	reporter.reportOperation(problem)
}

// agentEventPublisher owns Agent-scoped live notification publication. It
// does not own activity state or decide Turn outcomes.
type agentEventPublisher struct {
	subject  *ReactLoopAgent
	failures observerFailureReporter
}

func newAgentEventPublisher(
	subject *ReactLoopAgent,
	failures observerFailureReporter,
) *agentEventPublisher {
	return &agentEventPublisher{
		subject:  subject,
		failures: failures,
	}
}

func (publisher *agentEventPublisher) reportFailure(problem error) {
	publisher.failures.report(problem)
}

func (publisher *agentEventPublisher) publishStatus(destination agent.Status) {
	if err := publisher.subject.scopeRuntime.Dispatch(
		context.Background(),
		agent.StatusChanged{
			Subject: publisher.subject,
			Status:  destination,
		},
	); err != nil {
		publisher.reportFailure(fmt.Errorf(
			"agentloop: Agent %q status observer: %w",
			publisher.subject.identifier,
			err,
		))
	}
}

func (publisher *agentEventPublisher) publishError(
	requestContext context.Context,
	position activityPosition,
	problem error,
) {
	if requestContext == nil {
		requestContext = context.Background()
	}
	if err := publisher.subject.scopeRuntime.Dispatch(
		requestContext,
		agent.AgentError{
			Subject: publisher.subject,
			Turn:    position.turn,
			Step:    position.step,
			Err:     problem,
		},
	); err != nil {
		publisher.reportFailure(fmt.Errorf(
			"agentloop: Agent %q error observer: %w",
			publisher.subject.identifier,
			err,
		))
	}
}

func (publisher *agentEventPublisher) publishTurnStopping(
	requestContext context.Context,
	turn int64,
) error {
	return publisher.subject.scopeRuntime.Dispatch(
		requestContext,
		agent.TurnStopping{
			Subject: publisher.subject,
			Turn:    turn,
		},
	)
}

type inboxEventBridge struct {
	events *agentEventPublisher
}

func (bridge inboxEventBridge) Inserted(input agentmessage.UserMessage) {
	if err := bridge.events.subject.scopeRuntime.Dispatch(
		context.Background(),
		agent.InboxInserted{
			Subject: bridge.events.subject,
			Message: input,
		},
	); err != nil {
		bridge.events.reportFailure(err)
	}
}

func (bridge inboxEventBridge) Discarded(input agentmessage.UserMessage) {
	if err := bridge.events.subject.scopeRuntime.Dispatch(
		context.Background(),
		agent.InboxDiscarded{
			Subject: bridge.events.subject,
			Message: input,
		},
	); err != nil {
		bridge.events.reportFailure(err)
	}
}

func (bridge inboxEventBridge) Claimed(
	input agentmessage.UserMessage,
	turn int64,
) {
	if err := bridge.events.subject.scopeRuntime.Dispatch(
		context.Background(),
		agent.InboxClaimed{
			Subject: bridge.events.subject,
			Message: input,
			Turn:    turn,
		},
	); err != nil {
		bridge.events.reportFailure(err)
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
