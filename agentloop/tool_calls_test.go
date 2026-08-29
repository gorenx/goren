package agentloop

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/tools"
)

type silentInboxNotifications struct{}

func (silentInboxNotifications) Inserted(agentmessage.UserMessage)       {}
func (silentInboxNotifications) Discarded(agentmessage.UserMessage)      {}
func (silentInboxNotifications) Claimed(agentmessage.UserMessage, int64) {}

type failingToolScheduler struct {
	firstStarted chan struct{}
	releaseFirst chan struct{}
	failureSeen  chan struct{}
}

func (schedulerState *failingToolScheduler) Prepare(
	_ context.Context,
	input tools.ToolExecutionInput,
) (tools.ScheduledToolPreparation, error) {
	if input.CallID == "call-2" {
		close(schedulerState.failureSeen)
		return tools.ScheduledToolPreparation{}, errors.New(
			"scheduler preparation failed",
		)
	}
	return tools.ScheduledToolPreparation{
		Stage: tools.ScheduledDispatch,
		Execution: tools.ToolExecution{
			CallID: input.CallID,
			Name:   input.Name,
		},
	}, nil
}

func (schedulerState *failingToolScheduler) Dispatch(
	execution tools.ToolExecution,
) (tools.ScheduledToolDispatch, error) {
	if execution.CallID == "call-1" {
		close(schedulerState.firstStarted)
		<-schedulerState.releaseFirst
	}
	return tools.ScheduledToolDispatch{
		Result: &tools.ToolExecutionSuccess{
			Value: json.RawMessage(`{"ok":true}`),
			Content: []agentmessage.ContentBlock{
				agentmessage.NewTextBlock("ok"),
			},
		},
	}, nil
}

func (*failingToolScheduler) Finalize(
	tools.ToolExecution,
	tools.ToolExecutionResult,
) (tools.ToolExecutionResult, error) {
	return nil, errors.New("Finalize must not run after scheduler failure")
}

func (*failingToolScheduler) Finish(
	tools.ToolExecution,
	tools.ToolExecutionResult,
) tools.ToolExecutionResult {
	panic("Finish must not run after scheduler failure")
}

type schedulerToolRuntime struct {
	plugin.Base
	scheduler tools.ToolExecutionScheduler
}

func (*schedulerToolRuntime) Get(string) (tools.ToolDefinition, bool) {
	return tools.ToolDefinition{}, false
}

func (*schedulerToolRuntime) Schemas() []llm.ToolSchema {
	return nil
}

func (*schedulerToolRuntime) ExecutionMode(
	tools.ToolExecutionInput,
) tools.ToolExecutionMode {
	return tools.ExecutionParallel
}

func (runtimeState *schedulerToolRuntime) Scheduler() tools.ToolExecutionScheduler {
	return runtimeState.scheduler
}

func (*schedulerToolRuntime) Execute(
	context.Context,
	tools.ToolExecutionInput,
) tools.ToolExecutionResult {
	return nil
}

func TestSchedulerFailureStopsReplenishmentAndDrainsStartedDispatch(t *testing.T) {
	conversation, err := session.New(
		"scheduler-failure",
		session.CreateOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	subject := &ReactLoopAgent{
		identifier:   conversation.ID(),
		conversation: conversation,
	}
	pending, err := agent.NewInbox(conversation, silentInboxNotifications{})
	if err != nil {
		t.Fatal(err)
	}
	schedulerState := &failingToolScheduler{
		firstStarted: make(chan struct{}),
		releaseFirst: make(chan struct{}),
		failureSeen:  make(chan struct{}),
	}
	executor, err := newToolCallExecutor(
		subject,
		pending,
		&schedulerToolRuntime{
			scheduler: schedulerState,
		},
		2,
	)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, executeErr := executor.execute(
			context.Background(),
			1,
			1,
			[]agentmessage.ToolCallBlock{
				{
					ID:        "call-1",
					Name:      "parallel",
					Arguments: `{}`,
				},
				{
					ID:        "call-2",
					Name:      "parallel",
					Arguments: `{}`,
				},
				{
					ID:        "call-3",
					Name:      "parallel",
					Arguments: `{}`,
				},
			},
		)
		done <- executeErr
	}()
	select {
	case <-schedulerState.firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first dispatch did not start")
	}
	select {
	case <-schedulerState.failureSeen:
	case <-time.After(time.Second):
		t.Fatal("scheduler failure did not occur")
	}
	select {
	case executeErr := <-done:
		t.Fatalf("executor returned before started dispatch drained: %v", executeErr)
	case <-time.After(20 * time.Millisecond):
	}
	close(schedulerState.releaseFirst)
	select {
	case executeErr := <-done:
		if executeErr == nil || executeErr.Error() != "scheduler preparation failed" {
			t.Fatalf("execute error = %v", executeErr)
		}
	case <-time.After(time.Second):
		t.Fatal("executor did not settle after started dispatch drained")
	}
	callIDs, resultCount := internalToolEventSummary(t, conversation)
	if !reflect.DeepEqual(callIDs, []agentmessage.CallID{"call-1", "call-2"}) {
		t.Fatalf("started Tool calls = %#v", callIDs)
	}
	if resultCount != 0 {
		t.Fatalf("Tool result count = %d, want 0", resultCount)
	}
}

func internalToolEventSummary(
	t *testing.T,
	conversation session.Context,
) ([]agentmessage.CallID, int) {
	t.Helper()
	callIDs := make([]agentmessage.CallID, 0)
	resultCount := 0
	for _, committed := range conversation.Events() {
		switch committed.Type {
		case session.ToolCallEventName:
			var payload session.ToolCall
			if err := json.Unmarshal(committed.Data, &payload); err != nil {
				t.Fatal(err)
			}
			callIDs = append(callIDs, payload.CallID)
		case session.ToolResultEventName:
			resultCount++
		}
	}
	return callIDs, resultCount
}
