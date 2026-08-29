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

type cancelingPrepareScheduler struct {
	started chan struct{}
}

func (schedulerState *cancelingPrepareScheduler) Prepare(
	requestContext context.Context,
	_ tools.ToolExecutionInput,
) (tools.ScheduledToolPreparation, error) {
	close(schedulerState.started)
	<-requestContext.Done()
	return tools.ScheduledToolPreparation{}, requestContext.Err()
}

func (*cancelingPrepareScheduler) Dispatch(
	tools.ToolExecution,
) (tools.ScheduledToolDispatch, error) {
	panic("Dispatch must not run after canceled preparation")
}

func (*cancelingPrepareScheduler) Finalize(
	tools.ToolExecution,
	tools.ToolExecutionResult,
) (tools.ToolExecutionResult, error) {
	panic("Finalize must not run for a synthetic cancellation result")
}

func (*cancelingPrepareScheduler) Finish(
	tools.ToolExecution,
	tools.ToolExecutionResult,
) tools.ToolExecutionResult {
	panic("Finish must not run for a synthetic cancellation result")
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
		identifier:           conversation.ID(),
		conversation:         conversation,
		maxParallelToolCalls: 2,
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
	subject.pending = pending
	subject.toolRuntime = &schedulerToolRuntime{
		scheduler: schedulerState,
	}
	done := make(chan error, 1)
	go func() {
		_, executeErr := subject.executeToolBatch(
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

func TestCanceledPreparationPairsStartedAndUnstartedToolCalls(t *testing.T) {
	conversation, err := session.New(
		"prepare-cancellation",
		session.CreateOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	schedulerState := &cancelingPrepareScheduler{
		started: make(chan struct{}),
	}
	subject := &ReactLoopAgent{
		identifier:           conversation.ID(),
		conversation:         conversation,
		maxParallelToolCalls: 2,
		toolRuntime: &schedulerToolRuntime{
			scheduler: schedulerState,
		},
	}
	pending, err := agent.NewInbox(conversation, silentInboxNotifications{})
	if err != nil {
		t.Fatal(err)
	}
	subject.pending = pending
	runContext, cancelRun := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, executeErr := subject.executeToolBatch(
			runContext,
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
			},
		)
		done <- executeErr
	}()
	select {
	case <-schedulerState.started:
	case <-time.After(time.Second):
		t.Fatal("Tool preparation did not start")
	}
	cancelRun()
	select {
	case executeErr := <-done:
		if !errors.Is(executeErr, context.Canceled) {
			t.Fatalf("execute error = %v, want context cancellation", executeErr)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled Tool preparation did not settle")
	}
	callEvents := make([]session.Event, 0)
	resultEvents := make([]session.Event, 0)
	for _, committed := range conversation.Events() {
		switch committed.Type {
		case session.ToolCallEventName:
			callEvents = append(callEvents, committed)
		case session.ToolResultEventName:
			resultEvents = append(resultEvents, committed)
		}
	}
	if len(callEvents) != 2 || len(resultEvents) != 2 {
		t.Fatalf(
			"Tool boundaries = %d calls/%d results, want 2/2",
			len(callEvents),
			len(resultEvents),
		)
	}
	for index := range resultEvents {
		provenance := resultEvents[index].SourceEventSeqs
		if provenance == nil || len(*provenance) != 1 ||
			(*provenance)[0] != callEvents[index].Seq {
			t.Fatalf(
				"result %d provenance = %#v, want call seq %d",
				index,
				provenance,
				callEvents[index].Seq,
			)
		}
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
