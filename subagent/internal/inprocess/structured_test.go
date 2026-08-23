package inprocess

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/tools"
)

func TestScopedRuntimeAppendsDescriptorAndCommitsStructuredOutput(t *testing.T) {
	t.Parallel()
	promptSettings, err := systemprompt.ValidateConfig(systemprompt.Config{})
	if err != nil {
		t.Fatal(err)
	}
	toolSettings, err := tools.ValidateConfig(tools.Config{})
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := session.New("child", session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	childAgent := &runtimeAgent{
		id:      "child",
		session: conversation,
	}
	toolService := tools.New(toolSettings)
	descriptor := newDescriptorAppender(subagent.OneShotDescriptor{
		Provider: "spawn",
	})
	capture := newStructuredCapture(json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "answer": {"type": "string"}
  },
  "required": ["answer"]
}`))
	root := &runtimeRoot{
		children: []plugin.ChildPlugin{
			{
				Instance: systemprompt.New(
					promptSettings,
					systemprompt.RegistryOptions{},
				),
				Placement: plugin.SameScope,
			},
			{
				Instance:  toolService,
				Placement: plugin.SameScope,
			},
			{
				Instance:  childAgent,
				Placement: plugin.SameScope,
			},
			{
				Instance:  descriptor,
				Placement: plugin.SameScope,
			},
			{
				Instance:  capture,
				Placement: plugin.SameScope,
			},
		},
	}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{
		EventFailures: noEventFailures{},
	})
	if _, err = runtimeEngine.Start(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if shutdownErr := runtimeEngine.Shutdown(context.Background()); shutdownErr != nil {
			t.Errorf("shutdown: %v", shutdownErr)
		}
	})
	decision, err := agent.ResolvePreStep(
		context.Background(),
		agent.PreStepNotice{
			Subject: childAgent,
			Turn:    1,
			Step:    1,
		},
		agent.PreStepActionFunc(func(
			context.Context,
			agent.PreStepNotice,
		) (agent.PreStepDecision, error) {
			return agent.PreStepDecision{
				Kind: agent.PreStepEnter,
			}, nil
		}),
	)
	if err != nil || decision.Kind != agent.PreStepEnter {
		t.Fatalf("pre-step = %#v, error=%v", decision, err)
	}
	identity, found, foldErr := subagent.FoldDescriptor(conversation.Events())
	if foldErr != nil || !found || identity.ProviderName() != "spawn" {
		t.Fatalf("descriptor = %#v, found=%v, error=%v", identity, found, foldErr)
	}
	outcome := toolService.Execute(
		context.Background(),
		tools.ToolExecutionInput{
			CallID:     "structured-call",
			RootCallID: "structured-call",
			Name:       structuredOutputTool,
			Arguments:  json.RawMessage(`{"answer":"done"}`),
			Subject:    childAgent,
		},
	)
	if outcome.Failed() || !outcome.ConcludesAgentTurn() {
		t.Fatalf("structured outcome = %#v", outcome)
	}
	if string(capture.Captured()) != `{"answer":"done"}` {
		t.Fatalf("captured = %s", capture.Captured())
	}
}

type runtimeRoot struct {
	plugin.Base
	children []plugin.ChildPlugin
}

func (owner *runtimeRoot) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name:     "test/inprocess-runtime",
		Children: owner.children,
	}
}
func (*runtimeRoot) Apply(context.Context) error   { return nil }
func (*runtimeRoot) Dispose(context.Context) error { return nil }

type noEventFailures struct{}

func (noEventFailures) ReportEventFailure(
	context.Context,
	plugin.EventFailure,
) {
}

type runtimeAgent struct {
	plugin.Base
	id      session.SessionID
	session *session.Session
}

func (subject *runtimeAgent) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "test/inprocess-agent",
		Provides: []plugin.ProvidedService{
			plugin.NewProvidedService[agent.Agent](subject),
		},
	}
}
func (*runtimeAgent) Apply(context.Context) error                   { return nil }
func (*runtimeAgent) Dispose(context.Context) error                 { return nil }
func (subject *runtimeAgent) ID() session.SessionID                 { return subject.id }
func (*runtimeAgent) OptionsValue() agent.Options                   { return agent.Options{} }
func (subject *runtimeAgent) SessionValue() *session.Session        { return subject.session }
func (*runtimeAgent) InboxValue() *agent.Inbox                      { return nil }
func (*runtimeAgent) StatusValue() agent.Status                     { return agent.StatusIdle }
func (*runtimeAgent) Cancel(agent.CancelCause, agent.CancelOptions) {}
func (*runtimeAgent) WhenIdle(context.Context) error                { return nil }
func (*runtimeAgent) RunMaintenance(context.Context, agent.MaintenanceTask) error {
	return nil
}
func (*runtimeAgent) Send(llm.UserMessage, agent.InboxTarget, bool) error { return nil }
func (*runtimeAgent) Followup(llm.UserMessage) error                      { return nil }
func (*runtimeAgent) Steer(llm.UserMessage) error                         { return nil }
func (*runtimeAgent) Inject(llm.UserMessage) error                        { return nil }

var _ agent.Agent = (*runtimeAgent)(nil)
var _ plugin.EventFailureReporter = noEventFailures{}
