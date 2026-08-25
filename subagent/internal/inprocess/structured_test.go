package inprocess

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/tools"
)

type preStepEnterAction struct{}

func (preStepEnterAction) Execute(
	context.Context,
	agent.PreStepNotice,
) (agent.PreStepDecision, error) {
	return agent.PreStepDecision{
		Kind: agent.PreStepEnter,
	}, nil
}

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
	childAgent.runtime = &runtimeAgentEffects{
		source: childAgent,
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
		preStepEnterAction{},
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
	session session.Context
	runtime agent.AgentScopeRuntime
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
func (subject *runtimeAgent) SessionValue() session.Context         { return subject.session }
func (*runtimeAgent) InboxValue() *agent.Inbox                      { return nil }
func (*runtimeAgent) StatusValue() agent.Status                     { return agent.StatusIdle }
func (*runtimeAgent) Cancel(agent.CancelCause, agent.CancelOptions) {}
func (*runtimeAgent) WhenIdle(context.Context) error                { return nil }
func (*runtimeAgent) RunMaintenance(context.Context, func(context.Context) error) error {
	return nil
}
func (*runtimeAgent) Send(llm.UserMessage, agent.InboxTarget, bool) error { return nil }
func (*runtimeAgent) Followup(llm.UserMessage) error                      { return nil }
func (*runtimeAgent) Steer(llm.UserMessage) error                         { return nil }
func (*runtimeAgent) Inject(llm.UserMessage) error                        { return nil }
func (subject *runtimeAgent) ScopeRuntimeValue() agent.AgentScopeRuntime {
	return subject.runtime
}

type runtimeAgentEffects struct {
	source plugin.Plugin
}

func (effects *runtimeAgentEffects) Dispatch(
	requestContext context.Context,
	fact agent.RuntimeEvent,
) error {
	runtimeFact, matches := fact.(plugin.Event)
	if !matches {
		return errors.New("test: RuntimeEvent has no Plugin metadata")
	}
	return plugin.PublishEvent(requestContext, effects.source, runtimeFact)
}

func (effects *runtimeAgentEffects) ResolvePreStep(
	requestContext context.Context,
	notice agent.PreStepNotice,
	terminal agent.PreStepAction,
) (agent.PreStepDecision, error) {
	return plugin.Run(requestContext, effects.source, notice, terminal)
}

func (effects *runtimeAgentEffects) ResolveRequest(
	requestContext context.Context,
	notice agent.RequestNotice,
	terminal agent.RequestAction,
) (agent.RequestResolution, error) {
	return plugin.Run(requestContext, effects.source, notice, terminal)
}

func (effects *runtimeAgentEffects) ResolveRequestError(
	requestContext context.Context,
	notice agent.RequestErrorNotice,
	terminal agent.RequestErrorHandler,
) (agent.RequestErrorAction, error) {
	return plugin.Run(requestContext, effects.source, notice, terminal)
}

func (*runtimeAgentEffects) Provision(context.Context, agent.Provisioner) error {
	return nil
}

func (*runtimeAgentEffects) Teardown(context.Context) error { return nil }

var _ agent.Agent = (*runtimeAgent)(nil)
var _ agent.AgentScopeRuntime = (*runtimeAgentEffects)(nil)
var _ plugin.EventFailureReporter = noEventFailures{}
