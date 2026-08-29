package agentloop

import (
	"context"
	"fmt"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
)

// publishStatus announces one externally visible Agent status after the
// execution state has already entered that status.
func (subject *ReactLoopAgent) publishStatus(destination agent.Status) {
	if err := subject.scopeRuntime.Dispatch(
		context.Background(),
		agent.StatusChanged{
			Subject: subject,
			Status:  destination,
		},
	); err != nil {
		subject.observeError(fmt.Errorf(
			"agentloop: Agent %q status observer: %w",
			subject.identifier,
			err,
		))
	}
}

// publishError reports one contained Agent failure at its durable Turn and Step
// position. Observer failure never replaces the original operation failure.
func (subject *ReactLoopAgent) publishError(
	requestContext context.Context,
	turnNumber int64,
	stepNumber int64,
	problem error,
) {
	if requestContext == nil {
		requestContext = context.Background()
	}
	if err := subject.scopeRuntime.Dispatch(
		requestContext,
		agent.AgentError{
			Subject: subject,
			Turn:    turnNumber,
			Step:    stepNumber,
			Err:     problem,
		},
	); err != nil {
		subject.observeError(fmt.Errorf(
			"agentloop: Agent %q error observer: %w",
			subject.identifier,
			err,
		))
	}
}

// publishTurnStopping dispatches the ordered extension boundary before a Turn
// enters settlement.
func (subject *ReactLoopAgent) publishTurnStopping(
	requestContext context.Context,
	turnNumber int64,
) error {
	return subject.scopeRuntime.Dispatch(
		requestContext,
		agent.TurnStopping{
			Subject: subject,
			Turn:    turnNumber,
		},
	)
}

// inboxNotifications receives committed Inbox changes. Its Agent interface
// reference supplies canonical event identity, while runtime publishes the
// event. Neither reference grants lifecycle ownership.
type inboxNotifications struct {
	subject             agent.Agent
	runtime             agent.AgentScopeRuntime
	identifier          string
	reportObserverError func(error)
}

func (notifications inboxNotifications) Inserted(input agentmessage.UserMessage) {
	if notifications.subject == nil {
		return
	}
	if err := notifications.runtime.Dispatch(
		context.Background(),
		agent.InboxInserted{
			Subject: notifications.subject,
			Message: input,
		},
	); err != nil {
		notifications.report(fmt.Errorf(
			"agentloop: Agent %q Inbox inserted observer: %w",
			notifications.identifier,
			err,
		))
	}
}

func (notifications inboxNotifications) Discarded(input agentmessage.UserMessage) {
	if notifications.subject == nil {
		return
	}
	if err := notifications.runtime.Dispatch(
		context.Background(),
		agent.InboxDiscarded{
			Subject: notifications.subject,
			Message: input,
		},
	); err != nil {
		notifications.report(fmt.Errorf(
			"agentloop: Agent %q Inbox discarded observer: %w",
			notifications.identifier,
			err,
		))
	}
}

func (notifications inboxNotifications) Claimed(
	input agentmessage.UserMessage,
	turnNumber int64,
) {
	if notifications.subject == nil {
		return
	}
	if err := notifications.runtime.Dispatch(
		context.Background(),
		agent.InboxClaimed{
			Subject: notifications.subject,
			Message: input,
			Turn:    turnNumber,
		},
	); err != nil {
		notifications.report(fmt.Errorf(
			"agentloop: Agent %q Inbox claimed observer: %w",
			notifications.identifier,
			err,
		))
	}
}

func (notifications inboxNotifications) report(problem error) {
	if notifications.reportObserverError != nil && problem != nil {
		notifications.reportObserverError(problem)
	}
}

// agentCancellation preserves the canonical typed Agent cancellation as a
// Context cause without making execution state depend on Agent interfaces.
type agentCancellation struct {
	cause agent.CancelCause
}

func (problem agentCancellation) Error() string {
	if problem.cause == nil {
		return "agent canceled"
	}
	return "agent canceled: " + problem.cause.CancelKind()
}
