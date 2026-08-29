package agentloop

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
)

type blockingSession struct {
	session.Context
	blockOnce sync.Once
	started   chan struct{}
	release   chan struct{}
}

func (conversation *blockingSession) Commit(
	requestContext context.Context,
	plan session.WritePlan,
) (session.WriteResult, error) {
	blocked := false
	conversation.blockOnce.Do(func() {
		blocked = true
		close(conversation.started)
	})
	if blocked {
		select {
		case <-conversation.release:
		case <-requestContext.Done():
			return session.WriteResult{}, context.Cause(requestContext)
		}
	}
	return conversation.Context.Commit(requestContext, plan)
}

func TestRLAInvocationAdmittedBeforeCloseFinishesBeforeInboxClear(t *testing.T) {
	stored, err := session.New("send-before-close", session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	conversation := &blockingSession{
		Context: stored,
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	subject, err := newReactLoopAgent(
		conversation,
		agent.Options{},
		1,
		nil,
		benchmarkScopeRuntime{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err = subject.enterServing(); err != nil {
		t.Fatal(err)
	}
	input := newLifecycleMessage(t, "before-close")
	sendDone := make(chan error, 1)
	go func() {
		sendDone <- subject.Send(
			input,
			agent.NextTurn,
			false,
		)
	}()
	select {
	case <-conversation.started:
	case <-time.After(time.Second):
		t.Fatal("admitted Send did not reach Session commit")
	}
	closeDone := make(chan error, 1)
	go func() {
		closeDone <- subject.shutdown(context.Background())
	}()
	select {
	case closeErr := <-closeDone:
		t.Fatalf("close crossed an admitted Send: %v", closeErr)
	case <-time.After(20 * time.Millisecond):
	}
	close(conversation.release)
	select {
	case sendErr := <-sendDone:
		if sendErr != nil {
			t.Fatal(sendErr)
		}
	case <-time.After(time.Second):
		t.Fatal("admitted Send did not finish")
	}
	select {
	case closeErr := <-closeDone:
		if closeErr != nil {
			t.Fatal(closeErr)
		}
	case <-time.After(time.Second):
		t.Fatal("close did not finish after admitted Send")
	}
	if subject.pending.HasPending() {
		t.Fatal("close did not clear the message admitted before its cutoff")
	}
	splices := 0
	for _, committed := range conversation.Events() {
		if committed.Type == agent.InboxSplicedEventName {
			splices++
		}
	}
	if splices != 2 {
		t.Fatalf("Inbox splice count = %d, want append then clear", splices)
	}
}

func TestRLACloseCutoffRejectsLaterSend(t *testing.T) {
	conversation, err := session.New("close-before-send", session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	subject, err := newReactLoopAgent(
		conversation,
		agent.Options{},
		1,
		nil,
		benchmarkScopeRuntime{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err = subject.enterServing(); err != nil {
		t.Fatal(err)
	}
	if err = subject.shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = subject.shutdown(context.Background()); err != nil {
		t.Fatalf("repeated close = %v", err)
	}
	err = subject.Send(
		newLifecycleMessage(t, "after-close"),
		agent.NextTurn,
		false,
	)
	if err == nil || !strings.Contains(err.Error(), "not live") {
		t.Fatalf("Send after close error = %v", err)
	}
	if subject.pending.HasPending() {
		t.Fatal("Send after close reached Inbox")
	}
}

func TestRLAWaitersDoNotOwnExecutionCancellation(t *testing.T) {
	conversation, err := session.New("waiter-cancellation", session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	subject, err := newReactLoopAgent(
		conversation,
		agent.Options{},
		1,
		nil,
		benchmarkScopeRuntime{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err = subject.enterServing(); err != nil {
		t.Fatal(err)
	}
	runContext, cancelRun := context.WithCancelCause(context.Background())
	selected, err := subject.execution.EnterMaintenance(runContext, cancelRun)
	if err != nil {
		t.Fatal(err)
	}
	waitContext, cancelWait := context.WithCancel(context.Background())
	cancelWait()
	if err = subject.WhenIdle(waitContext); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled waiter error = %v", err)
	}
	if runContext.Err() != nil {
		t.Fatalf("waiter canceled Agent execution: %v", runContext.Err())
	}
	waiters := []chan error{
		make(chan error, 1),
		make(chan error, 1),
	}
	for _, waiter := range waiters {
		go func(result chan<- error) {
			result <- subject.WhenIdle(context.Background())
		}(waiter)
	}
	if err = subject.execution.EnterMaintenanceSettling(selected); err != nil {
		t.Fatal(err)
	}
	if err = subject.execution.EnterIdle(selected); err != nil {
		t.Fatal(err)
	}
	for index, waiter := range waiters {
		select {
		case waitErr := <-waiter:
			if waitErr != nil {
				t.Fatalf("waiter %d error = %v", index, waitErr)
			}
		case <-time.After(time.Second):
			t.Fatalf("waiter %d did not observe idle", index)
		}
	}
}

func TestRLASendAndCancelLinearizationRoutesCanceledInputToNextTurn(t *testing.T) {
	tests := []struct {
		name         string
		cancelFirst  bool
		wantNextTurn int
		wantNextStep int
	}{
		{
			name:         "Send before Cancel",
			wantNextStep: 1,
		},
		{
			name:         "Cancel before Send",
			cancelFirst:  true,
			wantNextTurn: 1,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			conversation, err := session.New(
				session.SessionID("send-cancel-"+testCase.name),
				session.CreateOptions{},
			)
			if err != nil {
				t.Fatal(err)
			}
			subject, err := newReactLoopAgent(
				conversation,
				agent.Options{},
				1,
				nil,
				benchmarkScopeRuntime{},
			)
			if err != nil {
				t.Fatal(err)
			}
			if err = subject.enterServing(); err != nil {
				t.Fatal(err)
			}
			runContext, cancelRun := context.WithCancelCause(
				context.Background(),
			)
			if _, err = subject.execution.EnterMaintenance(
				runContext,
				cancelRun,
			); err != nil {
				t.Fatal(err)
			}
			cancelAgent := func() {
				subject.Cancel(
					agent.UserCancel{},
					agent.CancelOptions{
						KeepInbox: true,
					},
				)
			}
			input := newLifecycleMessage(t, testCase.name)
			if testCase.cancelFirst {
				cancelAgent()
			}
			if err = subject.Send(input, agent.NextStep, true); err != nil {
				t.Fatal(err)
			}
			if !testCase.cancelFirst {
				cancelAgent()
			}
			if received := len(subject.pending.NextTurn()); received != testCase.wantNextTurn {
				t.Fatalf("next-turn count = %d, want %d", received, testCase.wantNextTurn)
			}
			if received := len(subject.pending.NextStep()); received != testCase.wantNextStep {
				t.Fatalf("next-step count = %d, want %d", received, testCase.wantNextStep)
			}
		})
	}
}

func newLifecycleMessage(
	testingState *testing.T,
	content string,
) agentmessage.UserMessage {
	testingState.Helper()
	message, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
		Content: []agentmessage.ContentBlock{
			agentmessage.NewTextBlock(content),
		},
		Source: agentmessage.UserMessageSource{},
	})
	if err != nil {
		testingState.Fatal(err)
	}
	return message
}
