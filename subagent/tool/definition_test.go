package tool

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
	oneShots := &oneShotRecord{
		run: &runRecord{
			id: "one-shot-child",
			result: subagent.Result{
				Output: []llm.ContentBlock{
					llm.NewTextBlock("foreground result"),
				},
				StopReason: subagent.StopCompleted,
			},
		},
	}
	continuations := &continuationRecord{}
	owner := &Plugin{
		settings: Settings{
			Provider:              "spawn",
			ToolName:              "subagent",
			EnableRunInBackground: true,
			BackgroundMode:        BackgroundContinuable,
		},
		oneShots:      oneShots,
		continuations: continuations,
	}
	rawValue, executeErr := owner.execute(
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
	if !oneShots.run.disposed || len(oneShots.starts) != 1 ||
		len(continuations.starts) != 0 ||
		!strings.Contains(string(rawValue), "foreground result") {
		t.Fatalf(
			"foreground route: value=%s starts=%d continuations=%d disposed=%v",
			rawValue,
			len(oneShots.starts),
			len(continuations.starts),
			oneShots.run.disposed,
		)
	}
	rawValue, executeErr = owner.execute(
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
	if len(continuations.starts) != 1 ||
		!strings.Contains(string(rawValue), `"kind":"continuable"`) {
		t.Fatalf(
			"continuable route: value=%s starts=%d",
			rawValue,
			len(continuations.starts),
		)
	}
}

func TestDelegationRejectsOneShotBackgroundWithoutJobs(t *testing.T) {
	t.Parallel()
	owner := &Plugin{
		settings: Settings{
			Provider:              "spawn",
			ToolName:              "subagent",
			EnableRunInBackground: true,
			BackgroundMode:        BackgroundOneShot,
		},
		oneShots: &oneShotRecord{
			run: &runRecord{},
		},
	}
	_, executeErr := owner.execute(
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

type oneShotRecord struct {
	run    *runRecord
	starts []subagent.StartRequest
}

func (record *oneShotRecord) Start(
	_ context.Context,
	_ string,
	request subagent.StartRequest,
) (subagent.Run, error) {
	record.starts = append(record.starts, request)
	return record.run, nil
}

type runRecord struct {
	id       session.SessionID
	result   subagent.Result
	disposed bool
}

func (record *runRecord) ID() session.SessionID {
	return record.id
}

func (*runRecord) LocalAgent() (agent.Agent, bool) {
	return nil, false
}

func (record *runRecord) AwaitResult(context.Context) (subagent.Result, error) {
	return record.result, nil
}

func (record *runRecord) Dispose(context.Context) error {
	record.disposed = true
	return nil
}

type continuationRecord struct {
	starts []subagent.ContinuableStartSpec
}

func (record *continuationRecord) StartContinuable(
	_ context.Context,
	request subagent.ContinuableStartSpec,
) (subagent.ContinuableStart, error) {
	record.starts = append(record.starts, request)
	return subagent.ContinuableStart{
		ChildID:   "continuable-child",
		MessageID: "message",
	}, nil
}

func (*continuationRecord) Followup(
	context.Context,
	agent.Agent,
	session.SessionID,
	[]llm.ContentBlock,
	subagent.FollowupOptions,
) (llm.MessageID, error) {
	return "", nil
}

func (*continuationRecord) Interrupt(
	session.SessionID,
	subagent.InterruptAuthority,
) error {
	return nil
}

func (*continuationRecord) ReportFrom(
	context.Context,
	agent.Agent,
	[]llm.ContentBlock,
	subagent.ReportOptions,
) (llm.MessageID, error) {
	return "", nil
}

func (*continuationRecord) DrainContinuableChildren(
	context.Context,
	agent.Agent,
	[]session.SessionID,
) error {
	return nil
}

func (*continuationRecord) DrainContinuableDescendants(
	context.Context,
	[]agent.Agent,
) error {
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

var _ subagent.OneShotService = (*oneShotRecord)(nil)
var _ subagent.ContinuableService = (*continuationRecord)(nil)
var _ subagent.Run = (*runRecord)(nil)
var _ agent.Agent = (*toolAgent)(nil)
