package execution

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

type terminatorRecord struct {
	mutex sync.Mutex
	calls []StopCause
	gate  chan struct{}
}

func (record *terminatorRecord) Terminate(
	_ context.Context,
	cause StopCause,
) (subagent.Terminal, error) {
	record.mutex.Lock()
	record.calls = append(record.calls, cause)
	record.mutex.Unlock()
	if record.gate != nil {
		<-record.gate
	}
	return subagent.Terminal{
		Output: []agentmessage.ContentBlock{
			agentmessage.NewTextBlock("done"),
		},
		StopReason: subagent.StopCompleted,
	}, nil
}

func TestExecutionUsesOneStateMachineAndOneTerminalTransaction(t *testing.T) {
	handler := &terminatorRecord{
		gate: make(chan struct{}),
	}
	running, err := New(
		subagent.RunID("run-1"),
		session.SessionID("child-1"),
		handler,
	)
	if err != nil {
		t.Fatal(err)
	}
	if running.State() != subagent.ExecutionStarting {
		t.Fatalf("initial state = %q", running.State())
	}
	if err := running.Activate(); err != nil {
		t.Fatal(err)
	}
	if running.State() != subagent.ExecutionActive {
		t.Fatalf("active state = %q", running.State())
	}
	if _, ready := running.Result(); ready {
		t.Fatal("Result was available before termination")
	}
	running.Stop(StopNormal)
	running.Stop(StopInterrupted)
	if running.State() != subagent.ExecutionStopping {
		t.Fatalf("stopping state = %q", running.State())
	}
	close(handler.gate)
	if waitErr := running.Wait(context.Background()); waitErr != nil {
		t.Fatal(waitErr)
	}
	terminalValue, ready := running.Result()
	if !ready {
		t.Fatal("Result was unavailable after Wait")
	}
	if terminalValue.StopReason != subagent.StopCompleted ||
		running.State() != subagent.ExecutionStopped {
		t.Fatalf(
			"terminal = %#v, state = %q",
			terminalValue,
			running.State(),
		)
	}
	cancelledContext, cancelWait := context.WithCancel(context.Background())
	cancelWait()
	if waitErr := running.Wait(cancelledContext); waitErr != nil {
		t.Fatalf("completed Wait error = %v", waitErr)
	}
	handler.mutex.Lock()
	defer handler.mutex.Unlock()
	if len(handler.calls) != 1 || handler.calls[0] != StopNormal {
		t.Fatalf("terminal calls = %#v", handler.calls)
	}
}

func TestWaitCancellationDoesNotStopExecution(t *testing.T) {
	running, err := New(
		subagent.RunID("run-2"),
		session.SessionID("child-2"),
		&terminatorRecord{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := running.Activate(); err != nil {
		t.Fatal(err)
	}
	waitContext, cancelWait := context.WithCancel(context.Background())
	cancelWait()
	err = running.Wait(waitContext)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait error = %v", err)
	}
	if running.State() != subagent.ExecutionActive {
		t.Fatalf("state after canceled wait = %q", running.State())
	}
	if err := running.Dispose(context.Background()); err != nil {
		t.Fatal(err)
	}
}
