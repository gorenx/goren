package delegation

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/tools"
)

func TestDelegationRoutesForegroundAndContinuableIndependently(t *testing.T) {
	t.Parallel()
	parentAgent := newToolAgent(t, "parent")
	oneShotExecution := &executionRecord{
		runID:   "one-shot-run",
		childID: "one-shot-child",
		terminal: subagent.Terminal{
			Output: []llm.ContentBlock{
				llm.NewTextBlock("foreground result"),
			},
			StopReason: subagent.StopCompleted,
		},
	}
	continuableExecution := &executionRecord{
		runID:   "continuable-run",
		childID: "continuable-child",
	}
	starter := &starterRecord{
		executions: map[subagent.Mode]subagent.Execution{
			subagent.ModeOneShot:     oneShotExecution,
			subagent.ModeContinuable: continuableExecution,
		},
	}
	adapter, err := newDelegationTool(
		Settings{
			Provider:              "spawn",
			ToolName:              "subagent",
			EnableRunInBackground: true,
			BackgroundMode:        BackgroundContinuable,
		},
		starter,
	)
	if err != nil {
		t.Fatal(err)
	}
	rawValue, executeErr := adapter.execute(
		json.RawMessage(`{
  "description": "review code",
  "prompt": "review the change",
  "run_in_background": false
}`),
		tools.ToolRunContext{
			Context: context.Background(),
			Execution: tools.ToolExecution{
				Subject: parentAgent,
			},
		},
	)
	if executeErr != nil {
		t.Fatal(executeErr)
	}
	if oneShotExecution.disposed || len(starter.commands) != 1 ||
		starter.commands[0].Mode() != subagent.ModeOneShot ||
		!strings.Contains(string(rawValue), "foreground result") {
		t.Fatalf(
			"foreground route: value=%s starts=%d disposed=%v",
			rawValue,
			len(starter.commands),
			oneShotExecution.disposed,
		)
	}
	rawValue, executeErr = adapter.execute(
		json.RawMessage(`{
  "description": "inspect tests",
  "prompt": "inspect the tests"
}`),
		tools.ToolRunContext{
			Context: context.Background(),
			Execution: tools.ToolExecution{
				Subject: parentAgent,
			},
		},
	)
	if executeErr != nil {
		t.Fatal(executeErr)
	}
	if len(starter.commands) != 2 ||
		starter.commands[1].Mode() != subagent.ModeContinuable ||
		!strings.Contains(string(rawValue), `"kind":"continuable"`) {
		t.Fatalf(
			"continuable route: value=%s starts=%d",
			rawValue,
			len(starter.commands),
		)
	}
}

func TestDelegationRejectsOneShotBackgroundWithoutJobs(t *testing.T) {
	t.Parallel()
	adapter, err := newDelegationTool(
		Settings{
			Provider:              "spawn",
			ToolName:              "subagent",
			EnableRunInBackground: true,
			BackgroundMode:        BackgroundOneShot,
		},
		&starterRecord{},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, executeErr := adapter.execute(
		json.RawMessage(`{
  "description": "review code",
  "prompt": "review the change",
  "run_in_background": true
}`),
		tools.ToolRunContext{
			Context: context.Background(),
			Execution: tools.ToolExecution{
				Subject: newToolAgent(t, "parent"),
			},
		},
	)
	if executeErr == nil || !strings.Contains(executeErr.Error(), "Jobs") {
		t.Fatalf("execute error = %v", executeErr)
	}
}

type starterRecord struct {
	commands   []subagent.StartCommand
	executions map[subagent.Mode]subagent.Execution
}

func (record *starterRecord) Start(
	_ context.Context,
	command subagent.StartCommand,
) (subagent.Execution, error) {
	record.commands = append(record.commands, command)
	return record.executions[command.Mode()], nil
}

type executionRecord struct {
	runID    subagent.RunID
	childID  session.SessionID
	terminal subagent.Terminal
	disposed bool
}

func (record *executionRecord) RunID() subagent.RunID {
	return record.runID
}

func (record *executionRecord) ChildID() session.SessionID {
	return record.childID
}

func (record *executionRecord) State() subagent.ExecutionState {
	if record.disposed {
		return subagent.ExecutionStopped
	}
	return subagent.ExecutionActive
}

func (record *executionRecord) AwaitTerminal(
	context.Context,
) (subagent.Terminal, error) {
	return record.terminal, nil
}

func (record *executionRecord) Dispose(context.Context) error {
	record.disposed = true
	return nil
}

type toolAgent struct {
	plugin.Base
	id      session.SessionID
	session session.Context
}

func newToolAgent(t *testing.T, identifier session.SessionID) *toolAgent {
	t.Helper()
	conversation, err := session.New(identifier, session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return &toolAgent{
		id:      identifier,
		session: conversation,
	}
}

func (*toolAgent) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "test/tool-agent",
	}
}
func (*toolAgent) Apply(context.Context) error                   { return nil }
func (*toolAgent) Dispose(context.Context) error                 { return nil }
func (subject *toolAgent) ID() session.SessionID                 { return subject.id }
func (*toolAgent) OptionsValue() agent.Options                   { return agent.Options{} }
func (subject *toolAgent) SessionValue() session.Context         { return subject.session }
func (*toolAgent) InboxValue() *agent.Inbox                      { return nil }
func (*toolAgent) StatusValue() agent.Status                     { return agent.StatusIdle }
func (*toolAgent) Cancel(agent.CancelCause, agent.CancelOptions) {}
func (*toolAgent) WhenIdle(context.Context) error                { return nil }
func (*toolAgent) RunMaintenance(context.Context, func(context.Context) error) error {
	return nil
}
func (*toolAgent) Send(llm.UserMessage, agent.InboxTarget, bool) error { return nil }
func (*toolAgent) Followup(llm.UserMessage) error                      { return nil }
func (*toolAgent) Steer(llm.UserMessage) error                         { return nil }
func (*toolAgent) Inject(llm.UserMessage) error                        { return nil }

var _ subagent.Starter = (*starterRecord)(nil)
var _ subagent.Execution = (*executionRecord)(nil)
var _ agent.Agent = (*toolAgent)(nil)
