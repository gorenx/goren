package tools_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/systemprompt"
	toolscore "github.com/gorenx/goren/tools"
)

type toolFixture struct {
	engine        *plugin.Runtime
	pluginScope   *plugin.Scope
	toolService   toolscore.ToolRuntime
	promptService systemprompt.SystemPrompt
	reporter      toolscore.ResultObserverReporter
}

type fixtureProvider struct {
	fixture *toolFixture
}

type observerReporter struct {
	count atomic.Int32
	seen  error
}

func (recorder *observerReporter) ReportToolObserverError(_ context.Context, _ toolscore.ToolExecution, observerErr error) {
	recorder.count.Add(1)
	recorder.seen = observerErr
}

func (*fixtureProvider) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "tools-fixture",
		Provides: []plugin.ServiceRef{
			systemprompt.Service.Ref(), toolscore.Service.Ref(),
		},
	}
}

func (instance *fixtureProvider) Apply(requestContext context.Context, pluginScope *plugin.Scope) error {
	promptSettings, err := systemprompt.ValidateConfig(systemprompt.Config{})
	if err != nil {
		return err
	}
	promptService, err := systemprompt.New(requestContext, pluginScope, promptSettings)
	if err != nil {
		return err
	}
	toolSettings, err := toolscore.ValidateConfig(toolscore.Config{})
	if err != nil {
		return err
	}
	toolService, err := toolscore.New(requestContext, pluginScope, promptService, instance.fixture.reporter, toolSettings)
	if err != nil {
		return err
	}
	if _, err := plugin.Provide(pluginScope, systemprompt.Service, promptService); err != nil {
		return err
	}
	if _, err := plugin.Provide(pluginScope, toolscore.Service, toolService); err != nil {
		return err
	}
	instance.fixture.pluginScope = pluginScope
	instance.fixture.promptService = promptService
	instance.fixture.toolService = toolService
	return nil
}

func newToolFixture(t *testing.T, observer toolscore.ResultObserverReporter) *toolFixture {
	t.Helper()
	state := &toolFixture{engine: plugin.NewRuntime(), reporter: observer}
	if _, err := state.engine.Load(context.Background(), &fixtureProvider{fixture: state}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := state.engine.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return state
}

func objectTool(name string, description string, body toolscore.Executor) toolscore.ToolDefinition {
	return toolscore.ToolDefinition{
		Name: name, Description: description,
		Parameters: json.RawMessage(`{"type":"object"}`),
		Output: toolscore.ToolOutputDefinition{
			Schema: json.RawMessage(`{"type":"object"}`),
			Renderer: toolscore.OutputRendererFunc(func(_ json.RawMessage, value json.RawMessage) ([]llm.ContentBlock, error) {
				return []llm.ContentBlock{llm.NewTextBlock(string(value))}, nil
			}),
		},
		Executor: body,
	}
}

func passThroughBody(arguments json.RawMessage, _ toolscore.ToolRunContext) (json.RawMessage, error) {
	return append(json.RawMessage(nil), arguments...), nil
}

func resultMessage(t *testing.T, outcome toolscore.ToolExecutionResult) string {
	t.Helper()
	blocks := outcome.ContentBlocks()
	if len(blocks) != 1 {
		t.Fatalf("content count = %d", len(blocks))
	}
	textBlock, ok := blocks[0].(llm.TextBlock)
	if !ok {
		t.Fatalf("content type = %T", blocks[0])
	}
	return textBlock.Text
}

func TestConfigPreservesOmissionAndRejectsUnavailablePresentation(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		input       string
		wantMessage string
	}{
		{input: `{}`, wantMessage: ""},
		{input: `{"mode":null}`, wantMessage: "mode must be"},
		{input: `{"mode":"code"}`, wantMessage: "Code Runtime bridge"},
		{input: `{"mode":"both"}`, wantMessage: "Code Runtime bridge"},
		{input: `{"mode":"other"}`, wantMessage: "unsupported presentation"},
		{input: `{"maxParallelSubCalls":0}`, wantMessage: "positive integer"},
		{input: `{"maxParallelSubCalls":null}`, wantMessage: "positive integer"},
		{input: `{"unknown":true}`, wantMessage: "unknown field"},
	} {
		testCase := testCase
		t.Run(testCase.input, func(t *testing.T) {
			var settings toolscore.Config
			err := json.Unmarshal([]byte(testCase.input), &settings)
			if err == nil {
				_, err = toolscore.ValidateConfig(settings)
			}
			if testCase.wantMessage == "" && err != nil {
				t.Fatal(err)
			}
			if testCase.wantMessage != "" && (err == nil || !strings.Contains(err.Error(), testCase.wantMessage)) {
				t.Fatalf("error = %v, want containing %q", err, testCase.wantMessage)
			}
		})
	}
}

func TestRegistryScopesRestrictionsShadowingAndPromptProjection(t *testing.T) {
	state := newToolFixture(t, nil)
	requestContext := context.Background()
	changeCount := 0
	if _, err := toolscore.OnChange(state.pluginScope, func(context.Context) error {
		changeCount++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	alphaRelease, err := state.toolService.Register(requestContext, state.pluginScope,
		objectTool("alpha", "global alpha", toolscore.ExecutorFunc(passThroughBody)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.toolService.Register(requestContext, state.pluginScope,
		objectTool("beta", "global beta", toolscore.ExecutorFunc(passThroughBody))); err != nil {
		t.Fatal(err)
	}
	childScope, _, err := state.pluginScope.Child("agent")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.toolService.Restrict(requestContext, childScope,
		toolscore.ToolRestriction{Allow: []string{"alpha"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := state.toolService.Register(requestContext, childScope,
		objectTool("beta", "scoped beta", toolscore.ExecutorFunc(passThroughBody))); err != nil {
		t.Fatal(err)
	}
	if _, err := state.toolService.Register(requestContext, childScope,
		objectTool("gamma", "scoped gamma", toolscore.ExecutorFunc(passThroughBody))); err != nil {
		t.Fatal(err)
	}
	projections := state.toolService.Schemas(childScope.Target())
	wantNames := []string{"alpha", "beta", "gamma"}
	gotNames := make([]string, 0, len(projections))
	for _, schema := range projections {
		gotNames = append(gotNames, schema.Name)
	}
	if !reflect.DeepEqual(gotNames, wantNames) || projections[1].Description != "scoped beta" {
		t.Fatalf("scoped schemas = %#v", projections)
	}
	if _, err := state.toolService.Restrict(requestContext, childScope,
		toolscore.ToolRestriction{Deny: []string{"gamma"}}); err == nil || !strings.Contains(err.Error(), "unknown global") {
		t.Fatalf("scope-local restriction error = %v", err)
	}
	assembled, err := state.promptService.Assemble(requestContext, systemprompt.AssembleContext{Scope: childScope.Target()})
	if err != nil {
		t.Fatal(err)
	}
	if len(assembled.Tools) != 3 || assembled.Tools[1].Name != "beta" {
		t.Fatalf("assembled tools = %#v", assembled.Tools)
	}
	assembled.Tools[0].Parameters[0] = '['
	if state.toolService.Schemas(childScope.Target())[0].Parameters[0] != '{' {
		t.Fatal("schema projection aliases registry storage")
	}
	if err := alphaRelease(requestContext); err != nil {
		t.Fatal(err)
	}
	if _, found := state.toolService.Get("alpha", childScope.Target()); found {
		t.Fatal("disposed inherited tool remains visible")
	}
	if changeCount != 6 {
		t.Fatalf("change count = %d, want 6", changeCount)
	}
}

func TestChangeFailureRollsBackRegistration(t *testing.T) {
	state := newToolFixture(t, nil)
	failChange := true
	if _, err := toolscore.OnChange(state.pluginScope, func(context.Context) error {
		if failChange {
			return errors.New("change rejected")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := state.toolService.Register(context.Background(), state.pluginScope,
		objectTool("rolled-back", "", toolscore.ExecutorFunc(passThroughBody))); err == nil {
		t.Fatal("registration with failed change observer succeeded")
	}
	if _, found := state.toolService.Get("rolled-back", plugin.ScopeKey{}); found {
		t.Fatal("failed registration leaked")
	}
	failChange = false
}

func TestExecutionPipelineOrderAndResultSnapshot(t *testing.T) {
	state := newToolFixture(t, nil)
	requestContext := context.Background()
	var orderMu sync.Mutex
	steps := make([]string, 0)
	record := func(label string) {
		orderMu.Lock()
		steps = append(steps, label)
		orderMu.Unlock()
	}
	if _, err := toolscore.OnPreExecute(state.pluginScope,
		func(chainContext context.Context, _ toolscore.ToolExecution, downstream toolscore.PreExecuteNext) (toolscore.PreToolDecision, error) {
			record("pre-before")
			decision, err := downstream(chainContext)
			record("pre-after")
			return decision, err
		}); err != nil {
		t.Fatal(err)
	}
	if _, err := state.toolService.Guard(state.pluginScope, toolscore.ToolGuardFunc(
		func(toolscore.ToolExecution) (string, bool) {
			record("guard")
			return "", false
		})); err != nil {
		t.Fatal(err)
	}
	if _, err := toolscore.OnExecute(state.pluginScope,
		func(chainContext context.Context, _ toolscore.ToolExecution, downstream toolscore.ExecuteNext) (toolscore.ToolExecutionResult, error) {
			record("execute-before")
			outcome, err := downstream(chainContext)
			record("execute-after")
			return outcome, err
		}); err != nil {
		t.Fatal(err)
	}
	if _, err := toolscore.OnPostExecute(state.pluginScope,
		func(chainContext context.Context, _ toolscore.ToolExecution, _ toolscore.ToolResultSnapshot, downstream toolscore.PostExecuteNext) (toolscore.PostToolDecision, error) {
			record("post-before")
			decision, err := downstream(chainContext)
			record("post-after")
			return decision, err
		}); err != nil {
		t.Fatal(err)
	}
	if _, err := toolscore.OnResult(state.pluginScope,
		func(_ context.Context, _ toolscore.ToolExecution, snapshot toolscore.ToolResultSnapshot) error {
			record("result")
			value, ok := snapshot.SuccessValue()
			if !ok || string(value) != `{"value":"ok"}` {
				t.Errorf("snapshot value = %s, %v", value, ok)
			}
			return nil
		}); err != nil {
		t.Fatal(err)
	}
	toolEntry := objectTool("pipeline", "", toolscore.ExecutorFunc(
		func(arguments json.RawMessage, _ toolscore.ToolRunContext) (json.RawMessage, error) {
			record("body")
			return arguments, nil
		}))
	toolEntry.Output.Renderer = toolscore.OutputRendererFunc(
		func(_ json.RawMessage, value json.RawMessage) ([]llm.ContentBlock, error) {
			record("render")
			return []llm.ContentBlock{llm.NewTextBlock(string(value))}, nil
		})
	if _, err := state.toolService.Register(requestContext, state.pluginScope, toolEntry); err != nil {
		t.Fatal(err)
	}
	outcome := state.toolService.Execute(requestContext, toolscore.ToolExecutionInput{
		CallID: "call-1", Name: "pipeline", Arguments: json.RawMessage(`{"value":"ok"}`),
	})
	if outcome.Failed() || resultMessage(t, outcome) != `{"value":"ok"}` {
		t.Fatalf("pipeline outcome = %#v", outcome)
	}
	want := []string{
		"pre-before", "pre-after", "guard", "execute-before", "body", "render",
		"execute-after", "post-before", "post-after", "result",
	}
	if !reflect.DeepEqual(steps, want) {
		t.Fatalf("pipeline order = %#v, want %#v", steps, want)
	}
}

func TestPolicyFailuresUnknownAndOutputValidationAreCanonical(t *testing.T) {
	state := newToolFixture(t, nil)
	requestContext := context.Background()
	childScope, _, err := state.pluginScope.Child("denied-agent")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := toolscore.OnPreExecute(childScope,
		func(context.Context, toolscore.ToolExecution, toolscore.PreExecuteNext) (toolscore.PreToolDecision, error) {
			return toolscore.DenyDecision{Reason: "policy denied"}, nil
		}); err != nil {
		t.Fatal(err)
	}
	var bodyCalls atomic.Int32
	if _, err := state.toolService.Register(requestContext, state.pluginScope,
		objectTool("guarded", "", toolscore.ExecutorFunc(
			func(json.RawMessage, toolscore.ToolRunContext) (json.RawMessage, error) {
				bodyCalls.Add(1)
				return json.RawMessage(`{}`), nil
			}))); err != nil {
		t.Fatal(err)
	}
	denied := state.toolService.Execute(requestContext, toolscore.ToolExecutionInput{
		CallID: "deny-1", Name: "guarded", Arguments: json.RawMessage(`{}`), Scope: childScope.Target(),
	})
	if !denied.Failed() || resultMessage(t, denied) != "Error: policy denied" || bodyCalls.Load() != 0 {
		t.Fatalf("denied outcome = %#v, body calls = %d", denied, bodyCalls.Load())
	}
	unknown := state.toolService.Execute(requestContext, toolscore.ToolExecutionInput{
		CallID: "unknown-1", Name: "absent", Arguments: json.RawMessage(`{}`),
	})
	if !unknown.Failed() || !strings.Contains(resultMessage(t, unknown), "unknown tool") {
		t.Fatalf("unknown outcome = %#v", unknown)
	}
	invalidTool := objectTool("invalid-output", "", toolscore.ExecutorFunc(
		func(json.RawMessage, toolscore.ToolRunContext) (json.RawMessage, error) {
			return json.RawMessage(`"wrong"`), nil
		}))
	if _, err := state.toolService.Register(requestContext, state.pluginScope, invalidTool); err != nil {
		t.Fatal(err)
	}
	invalid := state.toolService.Execute(requestContext, toolscore.ToolExecutionInput{
		CallID: "invalid-1", Name: "invalid-output", Arguments: json.RawMessage(`{}`),
	})
	if !invalid.Failed() || !strings.Contains(resultMessage(t, invalid), "invalid output") {
		t.Fatalf("invalid output outcome = %#v", invalid)
	}
}

func TestCallerCancellationSurvivesWrapperContextReplacement(t *testing.T) {
	state := newToolFixture(t, nil)
	cancelScope, _, err := state.pluginScope.Child("cancel-agent")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := toolscore.OnExecute(cancelScope,
		func(_ context.Context, _ toolscore.ToolExecution, downstream toolscore.ExecuteNext) (toolscore.ToolExecutionResult, error) {
			return downstream(context.Background())
		}); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	settled := make(chan struct{})
	if _, err := state.toolService.Register(context.Background(), state.pluginScope,
		objectTool("cancellable", "", toolscore.ExecutorFunc(
			func(_ json.RawMessage, runContext toolscore.ToolRunContext) (json.RawMessage, error) {
				close(started)
				<-runContext.Context.Done()
				close(settled)
				return json.RawMessage(`{}`), nil
			}))); err != nil {
		t.Fatal(err)
	}
	callerContext, cancel := context.WithCancel(context.Background())
	resultChannel := make(chan toolscore.ToolExecutionResult, 1)
	go func() {
		resultChannel <- state.toolService.Execute(callerContext, toolscore.ToolExecutionInput{
			CallID: "cancel-1", Name: "cancellable", Arguments: json.RawMessage(`{}`), Scope: cancelScope.Target(),
		})
	}()
	<-started
	cancel()
	outcome := <-resultChannel
	select {
	case <-settled:
	default:
		t.Fatal("Execute returned before tool body settled")
	}
	failure, ok := outcome.(*toolscore.ToolExecutionFailure)
	if !ok || failure.Error.Info == nil || failure.Error.Info.Code != toolscore.ToolAborted {
		t.Fatalf("cancellation outcome = %#v", outcome)
	}
}

func TestFinalizerRunsOnceAndClassifierFailsClosed(t *testing.T) {
	state := newToolFixture(t, nil)
	var finalizeCalls atomic.Int32
	toolEntry := objectTool("finalized", "", toolscore.ExecutorFunc(
		func(arguments json.RawMessage, runContext toolscore.ToolRunContext) (json.RawMessage, error) {
			runContext.ConcludeTurn()
			return arguments, nil
		}))
	toolEntry.FinalizeContent = toolscore.ContentFinalizerFunc(
		func(toolscore.ToolExecution, toolscore.ToolResultSnapshot) ([]llm.ContentBlock, bool) {
			finalizeCalls.Add(1)
			return []llm.ContentBlock{llm.NewTextBlock("final")}, true
		})
	toolEntry.ConcurrencyBehavior = toolscore.ConcurrencyClassifierFunc(func(json.RawMessage) bool {
		panic("classifier failure")
	})
	if _, err := state.toolService.Register(context.Background(), state.pluginScope, toolEntry); err != nil {
		t.Fatal(err)
	}
	if mode := state.toolService.ExecutionMode(toolscore.ToolExecutionInput{
		Name: "finalized", Arguments: json.RawMessage(`{}`),
	}); mode != toolscore.ExecutionExclusive {
		t.Fatalf("execution mode = %s", mode)
	}
	outcome := state.toolService.Execute(context.Background(), toolscore.ToolExecutionInput{
		CallID: "final-1", Name: "finalized", Arguments: json.RawMessage(`{}`),
	})
	success, ok := outcome.(*toolscore.ToolExecutionSuccess)
	if !ok || outcome.Failed() || resultMessage(t, outcome) != "final" ||
		finalizeCalls.Load() != 1 || !success.ConcludesTurn {
		t.Fatalf("finalized outcome = %#v, calls = %d", outcome, finalizeCalls.Load())
	}
	panicEntry := objectTool("panic-finalizer", "", toolscore.ExecutorFunc(passThroughBody))
	panicEntry.FinalizeContent = toolscore.ContentFinalizerFunc(
		func(toolscore.ToolExecution, toolscore.ToolResultSnapshot) ([]llm.ContentBlock, bool) {
			panic("finalizer failure")
		})
	if _, err := state.toolService.Register(context.Background(), state.pluginScope, panicEntry); err != nil {
		t.Fatal(err)
	}
	panicOutcome := state.toolService.Execute(context.Background(), toolscore.ToolExecutionInput{
		CallID: "panic-final-1", Name: "panic-finalizer", Arguments: json.RawMessage(`{}`),
	})
	if !panicOutcome.Failed() || !strings.Contains(resultMessage(t, panicOutcome), "finalizer panicked") {
		t.Fatalf("panic finalizer outcome = %#v", panicOutcome)
	}
}

func TestPolicyAndObserverSnapshotsAreDetachedAndObserverFailureIsContained(t *testing.T) {
	sentinel := errors.New("observer failed")
	recorder := &observerReporter{}
	state := newToolFixture(t, recorder)
	requestContext := context.Background()
	if _, err := toolscore.OnPreExecute(state.pluginScope,
		func(chainContext context.Context, execution toolscore.ToolExecution, downstream toolscore.PreExecuteNext) (toolscore.PreToolDecision, error) {
			arguments := execution.ArgumentsJSON()
			arguments[0] = '['
			return downstream(chainContext)
		}); err != nil {
		t.Fatal(err)
	}
	if _, err := toolscore.OnPostExecute(state.pluginScope,
		func(chainContext context.Context, _ toolscore.ToolExecution, snapshot toolscore.ToolResultSnapshot, downstream toolscore.PostExecuteNext) (toolscore.PostToolDecision, error) {
			value, ok := snapshot.SuccessValue()
			if !ok {
				t.Fatal("post snapshot does not contain successful value")
			}
			value[0] = '['
			second, ok := snapshot.SuccessValue()
			if !ok || second[0] != '{' {
				t.Fatal("post snapshot value accessor aliases retained data")
			}
			return downstream(chainContext)
		}); err != nil {
		t.Fatal(err)
	}
	if _, err := toolscore.OnResult(state.pluginScope,
		func(context.Context, toolscore.ToolExecution, toolscore.ToolResultSnapshot) error {
			return sentinel
		}); err != nil {
		t.Fatal(err)
	}
	if _, err := toolscore.OnResult(state.pluginScope,
		func(context.Context, toolscore.ToolExecution, toolscore.ToolResultSnapshot) error {
			panic("observer panic")
		}); err != nil {
		t.Fatal(err)
	}
	var received json.RawMessage
	if _, err := state.toolService.Register(requestContext, state.pluginScope,
		objectTool("detached", "", toolscore.ExecutorFunc(
			func(arguments json.RawMessage, _ toolscore.ToolRunContext) (json.RawMessage, error) {
				received = append(json.RawMessage(nil), arguments...)
				return arguments, nil
			}))); err != nil {
		t.Fatal(err)
	}
	outcome := state.toolService.Execute(requestContext, toolscore.ToolExecutionInput{
		CallID: "detached-1", Name: "detached", Arguments: json.RawMessage(`{"safe":true}`),
	})
	if outcome.Failed() || string(received) != `{"safe":true}` {
		t.Fatalf("detached outcome = %#v, arguments = %s", outcome, received)
	}
	if recorder.count.Load() != 1 || !errors.Is(recorder.seen, sentinel) ||
		!strings.Contains(recorder.seen.Error(), "result handler panicked") {
		t.Fatalf("observer reports = %d, error = %v", recorder.count.Load(), recorder.seen)
	}
}

func TestAroundDispatchAndPostDecisionsAreNormalized(t *testing.T) {
	state := newToolFixture(t, nil)
	requestContext := context.Background()
	if _, err := state.toolService.Register(requestContext, state.pluginScope,
		objectTool("normalized", "", toolscore.ExecutorFunc(passThroughBody))); err != nil {
		t.Fatal(err)
	}
	for _, testCase := range []struct {
		label       string
		decision    toolscore.PostToolDecision
		wantFailure bool
		wantMessage string
		wantValue   string
	}{
		{
			label: "content", decision: toolscore.ReplaceContentDecision{
				Content: []llm.ContentBlock{llm.NewTextBlock("replacement")},
			}, wantMessage: "replacement", wantValue: `{"original":true}`,
		},
		{
			label: "value", decision: toolscore.ReplaceValueDecision{
				Value: json.RawMessage(`{"changed":true}`),
			}, wantMessage: `{"changed":true}`, wantValue: `{"changed":true}`,
		},
		{
			label: "block", decision: toolscore.BlockDecision{
				Feedback: []llm.ContentBlock{llm.NewTextBlock("blocked")},
			}, wantFailure: true, wantMessage: "blocked",
		},
	} {
		testCase := testCase
		t.Run(testCase.label, func(t *testing.T) {
			childScope, childRelease, err := state.pluginScope.Child("post-" + testCase.label)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := childRelease(requestContext); err != nil {
					t.Error(err)
				}
			}()
			if _, err := toolscore.OnPostExecute(childScope,
				func(context.Context, toolscore.ToolExecution, toolscore.ToolResultSnapshot, toolscore.PostExecuteNext) (toolscore.PostToolDecision, error) {
					return testCase.decision, nil
				}); err != nil {
				t.Fatal(err)
			}
			outcome := state.toolService.Execute(requestContext, toolscore.ToolExecutionInput{
				CallID: llm.CallID("post-" + testCase.label), Name: "normalized",
				Arguments: json.RawMessage(`{"original":true}`), Scope: childScope.Target(),
			})
			if outcome.Failed() != testCase.wantFailure || resultMessage(t, outcome) != testCase.wantMessage {
				t.Fatalf("post outcome = %#v", outcome)
			}
			if testCase.wantValue != "" {
				success, ok := outcome.(*toolscore.ToolExecutionSuccess)
				if !ok || string(success.Value) != testCase.wantValue {
					t.Fatalf("post value = %#v", outcome)
				}
			}
		})
	}

	wrapperScope, _, err := state.pluginScope.Child("authored-wrapper")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := toolscore.OnExecute(wrapperScope,
		func(context.Context, toolscore.ToolExecution, toolscore.ExecuteNext) (toolscore.ToolExecutionResult, error) {
			return &toolscore.ToolExecutionSuccess{
				Value:   json.RawMessage(`{"wrapper":true}`),
				Content: []llm.ContentBlock{llm.NewTextBlock("untrusted content")},
			}, nil
		}); err != nil {
		t.Fatal(err)
	}
	authored := state.toolService.Execute(requestContext, toolscore.ToolExecutionInput{
		CallID: "wrapper-1", Name: "normalized", Arguments: json.RawMessage(`{}`), Scope: wrapperScope.Target(),
	})
	if authored.Failed() || resultMessage(t, authored) != `{"wrapper":true}` {
		t.Fatalf("authored wrapper outcome = %#v", authored)
	}
}
