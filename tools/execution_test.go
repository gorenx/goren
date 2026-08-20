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
	"github.com/gorenx/goren/tools"
)

type waterfallPlugin[
	I plugin.WaterfallInput,
	O plugin.WaterfallOutput,
] struct {
	plugin.Base
	name       string
	middleware plugin.WaterfallMiddleware[I, O]
}

func (owner *waterfallPlugin[I, O]) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: owner.name,
		Waterfalls: []plugin.WaterfallContribution{
			plugin.WaterfallOf[I, O](owner),
		},
	}
}

func (*waterfallPlugin[I, O]) Apply(context.Context) error {
	return nil
}

func (*waterfallPlugin[I, O]) Dispose(context.Context) error {
	return nil
}

func (owner *waterfallPlugin[I, O]) Intercept(
	requestContext context.Context,
	input I,
	downstream plugin.WaterfallAction[I, O],
) (O, error) {
	return owner.middleware.Intercept(requestContext, input, downstream)
}

type waterfallFunc[
	I plugin.WaterfallInput,
	O plugin.WaterfallOutput,
] func(context.Context, I, plugin.WaterfallAction[I, O]) (O, error)

func (operation waterfallFunc[I, O]) Intercept(
	requestContext context.Context,
	input I,
	downstream plugin.WaterfallAction[I, O],
) (O, error) {
	return operation(requestContext, input, downstream)
}

func resultText(
	testingContext *testing.T,
	outcome tools.ToolExecutionResult,
) string {
	testingContext.Helper()
	blocks := outcome.ContentBlocks()
	if len(blocks) != 1 {
		testingContext.Fatalf("content count = %d", len(blocks))
	}
	textBlock, matches := blocks[0].(llm.TextBlock)
	if !matches {
		testingContext.Fatalf("content type = %T", blocks[0])
	}
	return textBlock.Text
}

func toolContextMessage(
	testingContext *testing.T,
	textValue string,
	pluginName string,
) llm.UserMessage {
	testingContext.Helper()
	message, err := llm.NewUserMessage(llm.UserMessageInput{
		Content: []llm.ContentBlock{
			llm.NewTextBlock(textValue),
		},
		Source: llm.PluginMessageSource{
			Plugin: pluginName,
		},
	})
	if err != nil {
		testingContext.Fatal(err)
	}
	return message
}

func TestExecutionPipelineOrderAndDetachedResultEvent(t *testing.T) {
	var orderMutex sync.Mutex
	steps := make([]string, 0)
	record := func(label string) {
		orderMutex.Lock()
		steps = append(steps, label)
		orderMutex.Unlock()
	}
	prePolicy := &waterfallPlugin[
		tools.PreExecuteRequest,
		tools.PreExecuteOutcome,
	]{
		name: "pre-policy",
		middleware: waterfallFunc[
			tools.PreExecuteRequest,
			tools.PreExecuteOutcome,
		](func(
			requestContext context.Context,
			input tools.PreExecuteRequest,
			downstream plugin.WaterfallAction[
				tools.PreExecuteRequest,
				tools.PreExecuteOutcome,
			],
		) (tools.PreExecuteOutcome, error) {
			record("pre-before")
			arguments := input.Execution().ArgumentsJSON()
			arguments[0] = '['
			outcome, err := downstream.Execute(requestContext, input)
			record("pre-after")
			return outcome, err
		}),
	}
	dispatchPolicy := &waterfallPlugin[
		tools.ExecuteRequest,
		tools.ExecuteOutcome,
	]{
		name: "dispatch-policy",
		middleware: waterfallFunc[
			tools.ExecuteRequest,
			tools.ExecuteOutcome,
		](func(
			requestContext context.Context,
			input tools.ExecuteRequest,
			downstream plugin.WaterfallAction[
				tools.ExecuteRequest,
				tools.ExecuteOutcome,
			],
		) (tools.ExecuteOutcome, error) {
			record("execute-before")
			outcome, err := downstream.Execute(requestContext, input)
			record("execute-after")
			return outcome, err
		}),
	}
	postPolicy := &waterfallPlugin[
		tools.PostExecuteRequest,
		tools.PostExecuteOutcome,
	]{
		name: "post-policy",
		middleware: waterfallFunc[
			tools.PostExecuteRequest,
			tools.PostExecuteOutcome,
		](func(
			requestContext context.Context,
			input tools.PostExecuteRequest,
			downstream plugin.WaterfallAction[
				tools.PostExecuteRequest,
				tools.PostExecuteOutcome,
			],
		) (tools.PostExecuteOutcome, error) {
			record("post-before")
			value, available := input.Result().SuccessValue()
			if !available {
				return tools.PostExecuteOutcome{}, errors.New(
					"post policy did not receive success",
				)
			}
			value[0] = '['
			retained, available := input.Result().SuccessValue()
			if !available || retained[0] != '{' {
				return tools.PostExecuteOutcome{}, errors.New(
					"result snapshot aliases retained data",
				)
			}
			outcome, err := downstream.Execute(requestContext, input)
			record("post-after")
			return outcome, err
		}),
	}
	observer := &eventObserverPlugin{
		name: "result-observer",
		subscriptions: []plugin.EventSubscription{
			plugin.EventOf[tools.ExecutionCompleted](),
		},
		observe: func(_ context.Context, fact plugin.Event) error {
			completed, matches := fact.(tools.ExecutionCompleted)
			if !matches {
				return errors.New("unexpected Event type")
			}
			value, available := completed.Result().SuccessValue()
			if !available || string(value) != `{"value":"ok"}` {
				return errors.New("unexpected final value")
			}
			record("result")
			return nil
		},
	}
	state := newToolsFixture(
		t,
		prePolicy,
		dispatchPolicy,
		postPolicy,
		observer,
	)
	if err := state.service.AddGuard(
		context.Background(),
		"record",
		tools.ToolGuardFunc(func(tools.ToolExecution) (string, bool) {
			record("guard")
			return "", false
		}),
	); err != nil {
		t.Fatal(err)
	}
	definition := objectTool(
		"pipeline",
		"",
		tools.ExecutorFunc(func(
			arguments json.RawMessage,
			_ tools.ToolRunContext,
		) (json.RawMessage, error) {
			record("body")
			return arguments, nil
		}),
	)
	definition.Output.Renderer = tools.OutputRendererFunc(func(
		_ json.RawMessage,
		value json.RawMessage,
	) ([]llm.ContentBlock, error) {
		record("render")
		return []llm.ContentBlock{
			llm.NewTextBlock(string(value)),
		}, nil
	})
	if err := state.service.AddTool(context.Background(), definition); err != nil {
		t.Fatal(err)
	}
	outcome := state.service.Execute(
		context.Background(),
		tools.ToolExecutionInput{
			CallID:    "call-1",
			Name:      "pipeline",
			Arguments: json.RawMessage(`{"value":"ok"}`),
		},
	)
	if outcome.Failed() || resultText(t, outcome) != `{"value":"ok"}` {
		t.Fatalf("pipeline outcome = %#v", outcome)
	}
	want := []string{
		"pre-before",
		"pre-after",
		"guard",
		"execute-before",
		"body",
		"render",
		"execute-after",
		"post-before",
		"post-after",
		"result",
	}
	if !reflect.DeepEqual(steps, want) {
		t.Fatalf("pipeline order = %#v, want %#v", steps, want)
	}
}

func TestExecuteMiddlewareReceivesReadOnlyCanonicalSuccess(t *testing.T) {
	mutation := &waterfallPlugin[
		tools.ExecuteRequest,
		tools.ExecuteOutcome,
	]{
		name: "mutate-canonical-success",
		middleware: waterfallFunc[
			tools.ExecuteRequest,
			tools.ExecuteOutcome,
		](func(
			requestContext context.Context,
			input tools.ExecuteRequest,
			downstream plugin.WaterfallAction[
				tools.ExecuteRequest,
				tools.ExecuteOutcome,
			],
		) (tools.ExecuteOutcome, error) {
			outcome, err := downstream.Execute(requestContext, input)
			if err != nil {
				return tools.ExecuteOutcome{}, err
			}
			if _, mutable := outcome.Result.(*tools.ToolExecutionSuccess); mutable {
				return tools.ExecuteOutcome{}, errors.New(
					"canonical success exposed mutable result fields",
				)
			}
			return outcome, nil
		}),
	}
	state := newToolsFixture(t, mutation)
	if err := state.service.AddTool(
		context.Background(),
		objectTool(
			"sealed",
			"",
			tools.ExecutorFunc(passThroughBody),
		),
	); err != nil {
		t.Fatal(err)
	}
	outcome := state.service.Execute(
		context.Background(),
		tools.ToolExecutionInput{
			CallID:    "sealed-1",
			Name:      "sealed",
			Arguments: json.RawMessage(`{"value":"original"}`),
		},
	)
	succeeded, matches := outcome.(*tools.ToolExecutionSuccess)
	if !matches || string(succeeded.Value) != `{"value":"original"}` ||
		resultText(t, outcome) != `{"value":"original"}` || succeeded.ConcludesTurn {
		t.Fatalf("canonical result was mutated: %#v", outcome)
	}
}

func TestDeferredContextsPrecedeExecuteAndPostContexts(t *testing.T) {
	deferred := toolContextMessage(t, "deferred", "tool")
	wrapper := toolContextMessage(t, "wrapper", "wrapper")
	post := toolContextMessage(t, "post", "post")
	executePolicy := &waterfallPlugin[
		tools.ExecuteRequest,
		tools.ExecuteOutcome,
	]{
		name: "append-execute-context",
		middleware: waterfallFunc[
			tools.ExecuteRequest,
			tools.ExecuteOutcome,
		](func(
			requestContext context.Context,
			input tools.ExecuteRequest,
			downstream plugin.WaterfallAction[
				tools.ExecuteRequest,
				tools.ExecuteOutcome,
			],
		) (tools.ExecuteOutcome, error) {
			outcome, err := downstream.Execute(requestContext, input)
			if err != nil {
				return tools.ExecuteOutcome{}, err
			}
			value, succeeded := outcome.Result.SuccessValue()
			if !succeeded {
				return tools.ExecuteOutcome{}, errors.New("expected success value")
			}
			return tools.ExecuteOutcome{
				Result: &tools.ToolExecutionSuccess{
					Value: value,
					AdditionalContexts: []llm.UserMessage{
						wrapper,
					},
				},
			}, nil
		}),
	}
	postPolicy := &waterfallPlugin[
		tools.PostExecuteRequest,
		tools.PostExecuteOutcome,
	]{
		name: "append-post-context",
		middleware: waterfallFunc[
			tools.PostExecuteRequest,
			tools.PostExecuteOutcome,
		](func(
			context.Context,
			tools.PostExecuteRequest,
			plugin.WaterfallAction[
				tools.PostExecuteRequest,
				tools.PostExecuteOutcome,
			],
		) (tools.PostExecuteOutcome, error) {
			return tools.PostExecuteOutcome{
				Decision: tools.AcceptDecision{
					AdditionalContexts: []llm.UserMessage{
						post,
					},
				},
			}, nil
		}),
	}
	state := newToolsFixture(t, executePolicy, postPolicy)
	if err := state.service.AddTool(
		context.Background(),
		objectTool(
			"contexts",
			"",
			tools.ExecutorFunc(func(
				arguments json.RawMessage,
				runContext tools.ToolRunContext,
			) (json.RawMessage, error) {
				runContext.DeferContext(deferred)
				return arguments, nil
			}),
		),
	); err != nil {
		t.Fatal(err)
	}
	outcome := state.service.Execute(
		context.Background(),
		tools.ToolExecutionInput{
			CallID:    "contexts-1",
			Name:      "contexts",
			Arguments: json.RawMessage(`{}`),
		},
	)
	contexts := outcome.AdditionalContextMessages()
	actualIDs := make([]llm.MessageID, 0, len(contexts))
	for _, message := range contexts {
		actualIDs = append(actualIDs, message.StableID())
	}
	wantIDs := []llm.MessageID{
		deferred.StableID(),
		wrapper.StableID(),
		post.StableID(),
	}
	if !reflect.DeepEqual(actualIDs, wantIDs) {
		t.Fatalf("context order = %#v, want %#v", actualIDs, wantIDs)
	}
}

func TestFinishMaterializesNilStagedResult(t *testing.T) {
	state := newToolsFixture(t)
	if err := state.service.AddTool(
		context.Background(),
		objectTool(
			"staged",
			"",
			tools.ExecutorFunc(passThroughBody),
		),
	); err != nil {
		t.Fatal(err)
	}
	scheduler := state.service.Scheduler()
	preparation, err := scheduler.Prepare(
		context.Background(),
		tools.ToolExecutionInput{
			CallID:    "",
			Name:      "staged",
			Arguments: json.RawMessage(`{}`),
		},
	)
	if err != nil || preparation.Stage != tools.ScheduledFinalResult {
		t.Fatalf("preparation = %#v, error = %v", preparation, err)
	}
	outcome := scheduler.Finish(preparation.Execution, nil)
	if !outcome.Failed() || !strings.Contains(resultText(t, outcome), "staged result is nil") {
		t.Fatalf("nil staged result = %#v", outcome)
	}
}

func TestSchedulerConsumesEachPreparedStageOnce(t *testing.T) {
	var bodyCalls atomic.Int32
	var finalizerCalls atomic.Int32
	definition := objectTool(
		"single-use-stage",
		"",
		tools.ExecutorFunc(func(
			arguments json.RawMessage,
			_ tools.ToolRunContext,
		) (json.RawMessage, error) {
			bodyCalls.Add(1)
			return arguments, nil
		}),
	)
	definition.FinalizeContent = tools.ContentFinalizerFunc(func(
		tools.ToolExecution,
		tools.ToolResultSnapshot,
	) ([]llm.ContentBlock, bool) {
		finalizerCalls.Add(1)
		return nil, false
	})
	state := newToolsFixture(t)
	if err := state.service.AddTool(
		context.Background(),
		definition,
	); err != nil {
		t.Fatal(err)
	}
	scheduler := state.service.Scheduler()
	preparation, err := scheduler.Prepare(
		context.Background(),
		tools.ToolExecutionInput{
			CallID:    "single-use-1",
			Name:      "single-use-stage",
			Arguments: json.RawMessage(`{}`),
		},
	)
	if err != nil || preparation.Stage != tools.ScheduledDispatch {
		t.Fatalf("preparation = %#v, error = %v", preparation, err)
	}
	dispatched, err := scheduler.Dispatch(preparation.Execution)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scheduler.Dispatch(preparation.Execution); err == nil ||
		!strings.Contains(err.Error(), "not prepared for dispatch") {
		t.Fatalf("second Dispatch error = %v", err)
	}
	finalOutcome, err := scheduler.Finalize(
		preparation.Execution,
		dispatched.Result,
	)
	if err != nil || finalOutcome.Failed() {
		t.Fatalf("final outcome = %#v, error = %v", finalOutcome, err)
	}
	if _, err := scheduler.Finalize(
		preparation.Execution,
		dispatched.Result,
	); err == nil || !strings.Contains(err.Error(), "not prepared") {
		t.Fatalf("second Finalize error = %v", err)
	}
	if bodyCalls.Load() != 1 || finalizerCalls.Load() != 1 {
		t.Fatalf(
			"body calls = %d, finalizer calls = %d",
			bodyCalls.Load(),
			finalizerCalls.Load(),
		)
	}
}

func TestExecutionFailuresAreCanonical(t *testing.T) {
	state := newToolsFixture(t)
	unknown := state.service.Execute(
		context.Background(),
		tools.ToolExecutionInput{
			CallID:    "unknown-1",
			Name:      "absent",
			Arguments: json.RawMessage(`{}`),
		},
	)
	if !unknown.Failed() || !strings.Contains(resultText(t, unknown), "unknown tool") {
		t.Fatalf("unknown outcome = %#v", unknown)
	}
	invalid := objectTool(
		"invalid-output",
		"",
		tools.ExecutorFunc(func(
			json.RawMessage,
			tools.ToolRunContext,
		) (json.RawMessage, error) {
			return json.RawMessage(`"wrong"`), nil
		}),
	)
	if err := state.service.AddTool(context.Background(), invalid); err != nil {
		t.Fatal(err)
	}
	invalidOutcome := state.service.Execute(
		context.Background(),
		tools.ToolExecutionInput{
			CallID:    "invalid-1",
			Name:      "invalid-output",
			Arguments: json.RawMessage(`{}`),
		},
	)
	if !invalidOutcome.Failed() ||
		!strings.Contains(resultText(t, invalidOutcome), "invalid output") {
		t.Fatalf("invalid output outcome = %#v", invalidOutcome)
	}
}

func TestCallerCancellationSurvivesMiddlewareContextReplacement(t *testing.T) {
	dispatchPolicy := &waterfallPlugin[
		tools.ExecuteRequest,
		tools.ExecuteOutcome,
	]{
		name: "replace-context",
		middleware: waterfallFunc[
			tools.ExecuteRequest,
			tools.ExecuteOutcome,
		](func(
			_ context.Context,
			input tools.ExecuteRequest,
			downstream plugin.WaterfallAction[
				tools.ExecuteRequest,
				tools.ExecuteOutcome,
			],
		) (tools.ExecuteOutcome, error) {
			return downstream.Execute(context.Background(), input)
		}),
	}
	state := newToolsFixture(t, dispatchPolicy)
	started := make(chan struct{})
	settled := make(chan struct{})
	if err := state.service.AddTool(
		context.Background(),
		objectTool(
			"cancellable",
			"",
			tools.ExecutorFunc(func(
				_ json.RawMessage,
				runContext tools.ToolRunContext,
			) (json.RawMessage, error) {
				close(started)
				<-runContext.Context.Done()
				close(settled)
				return json.RawMessage(`{}`), nil
			}),
		),
	); err != nil {
		t.Fatal(err)
	}
	callerContext, cancelCaller := context.WithCancel(context.Background())
	resultChannel := make(chan tools.ToolExecutionResult, 1)
	go func() {
		resultChannel <- state.service.Execute(
			callerContext,
			tools.ToolExecutionInput{
				CallID:    "cancel-1",
				Name:      "cancellable",
				Arguments: json.RawMessage(`{}`),
			},
		)
	}()
	<-started
	cancelCaller()
	outcome := <-resultChannel
	select {
	case <-settled:
	default:
		t.Fatal("Execute returned before Tool body settled")
	}
	failure, matches := outcome.(*tools.ToolExecutionFailure)
	if !matches || failure.Error.Info == nil ||
		failure.Error.Info.Code != tools.ToolAborted {
		t.Fatalf("cancellation outcome = %#v", outcome)
	}
}

func TestFinalizerRunsOnceAndClassifierFailsClosed(t *testing.T) {
	state := newToolsFixture(t)
	var finalizeCalls atomic.Int32
	definition := objectTool(
		"finalized",
		"",
		tools.ExecutorFunc(func(
			arguments json.RawMessage,
			runContext tools.ToolRunContext,
		) (json.RawMessage, error) {
			runContext.ConcludeTurn()
			return arguments, nil
		}),
	)
	definition.FinalizeContent = tools.ContentFinalizerFunc(func(
		tools.ToolExecution,
		tools.ToolResultSnapshot,
	) ([]llm.ContentBlock, bool) {
		finalizeCalls.Add(1)
		return []llm.ContentBlock{
			llm.NewTextBlock("final"),
		}, true
	})
	definition.ConcurrencyBehavior = tools.ConcurrencyClassifierFunc(func(
		json.RawMessage,
	) bool {
		panic("classifier failure")
	})
	if err := state.service.AddTool(context.Background(), definition); err != nil {
		t.Fatal(err)
	}
	if mode := state.service.ExecutionMode(tools.ToolExecutionInput{
		Name:      "finalized",
		Arguments: json.RawMessage(`{}`),
	}); mode != tools.ExecutionExclusive {
		t.Fatalf("execution mode = %s", mode)
	}
	outcome := state.service.Execute(
		context.Background(),
		tools.ToolExecutionInput{
			CallID:    "final-1",
			Name:      "finalized",
			Arguments: json.RawMessage(`{}`),
		},
	)
	success, matches := outcome.(*tools.ToolExecutionSuccess)
	if !matches || outcome.Failed() || resultText(t, outcome) != "final" ||
		finalizeCalls.Load() != 1 || !success.ConcludesTurn {
		t.Fatalf(
			"finalized outcome = %#v, calls = %d",
			outcome,
			finalizeCalls.Load(),
		)
	}
}
