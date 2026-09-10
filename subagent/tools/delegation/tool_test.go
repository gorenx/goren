package delegation

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/tools"
)

type starterRecord struct {
	commands   []subagent.StartCommand
	executions map[subagent.Mode]subagent.Execution
}

func (record *starterRecord) Start(_ context.Context, command subagent.StartCommand) (subagent.Execution, error) {
	record.commands = append(record.commands, command)
	return record.executions[command.Mode()], nil
}

type executionRecord struct {
	runID    subagent.RunID
	childID  session.SessionID
	terminal subagent.Terminal
}

func (record *executionRecord) RunID() subagent.RunID             { return record.runID }
func (record *executionRecord) ChildID() session.SessionID        { return record.childID }
func (*executionRecord) State() subagent.ExecutionState           { return subagent.ExecutionActive }
func (*executionRecord) Wait(context.Context) error               { return nil }
func (record *executionRecord) Result() (subagent.Terminal, bool) { return record.terminal, true }
func (*executionRecord) Dispose(context.Context) error            { return nil }

func TestDelegationUsesInvocationSessionForForegroundAndBackground(t *testing.T) {
	foreground := &executionRecord{
		runID:   "foreground-run",
		childID: "foreground-child",
		terminal: subagent.Terminal{
			Output: []agentmessage.ContentBlock{
				agentmessage.NewTextBlock("foreground result"),
			},
			StopReason: subagent.StopCompleted,
		},
	}
	background := &executionRecord{
		runID:   "background-run",
		childID: "background-child",
	}
	starter := &starterRecord{
		executions: map[subagent.Mode]subagent.Execution{
			subagent.ModeOneShot:     foreground,
			subagent.ModeContinuable: background,
		},
	}
	adapter, err := New(Settings{
		SeedBuilder:           "spawn",
		ToolName:              "subagent",
		EnableRunInBackground: true,
		BackgroundMode:        BackgroundContinuable,
	}, starter)
	if err != nil {
		t.Fatal(err)
	}
	run := tools.ToolRunContext{
		Context: context.Background(),
		Execution: tools.Invocation{
			SessionID: "parent",
		},
	}
	value, err := adapter.Execute(json.RawMessage(`{"description":"review","prompt":"inspect","run_in_background":false}`), run)
	if err != nil || !strings.Contains(string(value), "foreground result") {
		t.Fatalf("foreground value = %s, error = %v", value, err)
	}
	foregroundCommand, valid := starter.commands[0].(subagent.OneShotStartCommand)
	if !valid {
		t.Fatalf("command = %T", starter.commands[0])
	}
	foregroundRequest, err := foregroundCommand.Snapshot()
	if err != nil || foregroundRequest.ParentID != "parent" {
		t.Fatalf("request = %#v, error = %v", foregroundRequest, err)
	}
	value, err = adapter.Execute(json.RawMessage(`{"description":"research","prompt":"inspect"}`), run)
	if err != nil || !strings.Contains(string(value), `"subagentId":"background-child"`) {
		t.Fatalf("background value = %s, error = %v", value, err)
	}
	backgroundCommand, valid := starter.commands[1].(subagent.ContinuableStartCommand)
	if !valid {
		t.Fatalf("command = %T", starter.commands[1])
	}
	backgroundRequest, err := backgroundCommand.Snapshot()
	if err != nil || backgroundRequest.ParentID != "parent" {
		t.Fatalf("request = %#v, error = %v", backgroundRequest, err)
	}
}
