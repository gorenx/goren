package execution

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/tools"
)

type policyFixture[
	I any,
	O any,
] struct {
	name       string
	middleware middleware[I, O]
}

func (owner *policyFixture[I, O]) Intercept(
	requestContext context.Context,
	input I,
	downstream action[I, O],
) (O, error) {
	return owner.middleware.Intercept(requestContext, input, downstream)
}

type policyFunc[
	I any,
	O any,
] func(context.Context, I, action[I, O]) (O, error)

func (operation policyFunc[I, O]) Intercept(
	requestContext context.Context,
	input I,
	downstream action[I, O],
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
	textBlock, matches := blocks[0].(agentmessage.TextBlock)
	if !matches {
		testingContext.Fatalf("content type = %T", blocks[0])
	}
	return textBlock.Text
}

func toolContextMessage(
	testingContext *testing.T,
	textValue string,
	pluginName string,
) agentmessage.UserMessage {
	testingContext.Helper()
	message, err := agentmessage.NewUserMessage(agentmessage.UserMessageInput{
		Content: []agentmessage.ContentBlock{
			agentmessage.NewTextBlock(textValue),
		},
		Source: agentmessage.PluginMessageSource{
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
	prePolicy := &policyFixture[
		tools.PreExecuteRequest,
		tools.PreExecuteOutcome,
	]{
		name: "pre-policy",
		middleware: policyFunc[
			tools.PreExecuteRequest,
			tools.PreExecuteOutcome,
		](func(
			requestContext context.Context,
			input tools.PreExecuteRequest,
			downstream action[
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
	dispatchPolicy := &policyFixture[
		tools.ExecuteRequest,
		tools.ExecuteOutcome,
	]{
		name: "dispatch-policy",
		middleware: policyFunc[
			tools.ExecuteRequest,
			tools.ExecuteOutcome,
		](func(
			requestContext context.Context,
			input tools.ExecuteRequest,
			downstream action[
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
	postPolicy := &policyFixture[
		tools.PostExecuteRequest,
		tools.PostExecuteOutcome,
	]{
		name: "post-policy",
		middleware: policyFunc[
			tools.PostExecuteRequest,
			tools.PostExecuteOutcome,
		](func(
			requestContext context.Context,
			input tools.PostExecuteRequest,
			downstream action[
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
	observer := &eventObserver{
		name: "result-observer",
		subscriptions: []string{
			tools.ResultEventName,
		},
		observe: func(_ context.Context, fact toolEvent) error {
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
	state.policies.Guards = []tools.ToolGuard{tools.ToolGuardFunc(func(tools.ToolExecution) (string, bool) {
		record("guard")
		return "", false
	})}
	definition := objectTool(
		"pipeline",
		"",
		executorStub(func(
			arguments json.RawMessage,
			_ tools.ToolRunContext,
		) (json.RawMessage, error) {
			record("body")
			return arguments, nil
		}),
	)
	definition.Output.Renderer = outputRendererStub(func(
		_ json.RawMessage,
		value json.RawMessage,
	) ([]agentmessage.ContentBlock, error) {
		record("render")
		return []agentmessage.ContentBlock{
			agentmessage.NewTextBlock(string(value)),
		}, nil
	})
	coordinator := testScheduler(t, definition)
	outcome := executeCall(t, coordinator,
		context.Background(),
		tools.ToolExecutionInput{
			CallID:    "call-1",
			Name:      "pipeline",
			Arguments: json.RawMessage(`{"value":"ok"}`),
		}, state.policies,
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
	mutation := &policyFixture[
		tools.ExecuteRequest,
		tools.ExecuteOutcome,
	]{
		name: "mutate-canonical-success",
		middleware: policyFunc[
			tools.ExecuteRequest,
			tools.ExecuteOutcome,
		](func(
			requestContext context.Context,
			input tools.ExecuteRequest,
			downstream action[
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
	coordinator := testScheduler(t, objectTool(
		"sealed",
		"",
		executorStub(passThroughBody),
	))
	outcome := executeCall(t, coordinator,
		context.Background(),
		tools.ToolExecutionInput{
			CallID:    "sealed-1",
			Name:      "sealed",
			Arguments: json.RawMessage(`{"value":"original"}`),
		}, state.policies,
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
	executePolicy := &policyFixture[
		tools.ExecuteRequest,
		tools.ExecuteOutcome,
	]{
		name: "append-execute-context",
		middleware: policyFunc[
			tools.ExecuteRequest,
			tools.ExecuteOutcome,
		](func(
			requestContext context.Context,
			input tools.ExecuteRequest,
			downstream action[
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
					AdditionalContexts: []agentmessage.UserMessage{
						wrapper,
					},
				},
			}, nil
		}),
	}
	postPolicy := &policyFixture[
		tools.PostExecuteRequest,
		tools.PostExecuteOutcome,
	]{
		name: "append-post-context",
		middleware: policyFunc[
			tools.PostExecuteRequest,
			tools.PostExecuteOutcome,
		](func(
			context.Context,
			tools.PostExecuteRequest,
			action[
				tools.PostExecuteRequest,
				tools.PostExecuteOutcome,
			],
		) (tools.PostExecuteOutcome, error) {
			return tools.PostExecuteOutcome{
				Decision: tools.AcceptDecision{
					AdditionalContexts: []agentmessage.UserMessage{
						post,
					},
				},
			}, nil
		}),
	}
	state := newToolsFixture(t, executePolicy, postPolicy)
	coordinator := testScheduler(t, objectTool(
		"contexts",
		"",
		executorStub(func(
			arguments json.RawMessage,
			runContext tools.ToolRunContext,
		) (json.RawMessage, error) {
			runContext.DeferContext(deferred)
			return arguments, nil
		}),
	))
	outcome := executeCall(t, coordinator,
		context.Background(),
		tools.ToolExecutionInput{
			CallID:    "contexts-1",
			Name:      "contexts",
			Arguments: json.RawMessage(`{}`),
		}, state.policies,
	)
	contexts := outcome.AdditionalContextMessages()
	actualIDs := make([]agentmessage.MessageID, 0, len(contexts))
	for _, message := range contexts {
		actualIDs = append(actualIDs, message.StableID())
	}
	wantIDs := []agentmessage.MessageID{
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
	coordinator := testScheduler(t, objectTool(
		"staged",
		"",
		executorStub(passThroughBody),
	))
	scheduler := coordinator
	preparation, err := scheduler.prepare(
		context.Background(),
		tools.ToolExecutionInput{
			CallID:    "",
			Name:      "staged",
			Arguments: json.RawMessage(`{}`),
		}, state.policies,
	)
	if err != nil || preparation.Stage != tools.ScheduledFinalResult {
		t.Fatalf("preparation = %#v, error = %v", preparation, err)
	}
	outcome := (&Call{
		scheduler:   scheduler,
		preparation: preparation,
	}).finish(nil)
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
		executorStub(func(
			arguments json.RawMessage,
			_ tools.ToolRunContext,
		) (json.RawMessage, error) {
			bodyCalls.Add(1)
			return arguments, nil
		}),
	)
	definition.FinalizeContent = contentFinalizerStub(func(
		tools.ToolExecution,
		tools.ToolResultSnapshot,
	) ([]agentmessage.ContentBlock, bool) {
		finalizerCalls.Add(1)
		return nil, false
	})
	state := newToolsFixture(t)
	coordinator := testScheduler(t, definition)
	scheduler := coordinator
	preparation, err := scheduler.prepare(
		context.Background(),
		tools.ToolExecutionInput{
			CallID:    "single-use-1",
			Name:      "single-use-stage",
			Arguments: json.RawMessage(`{}`),
		}, state.policies,
	)
	if err != nil || preparation.Stage != tools.ScheduledDispatch {
		t.Fatalf("preparation = %#v, error = %v", preparation, err)
	}
	dispatched, err := (&Call{
		scheduler:   scheduler,
		preparation: preparation,
	}).dispatchBody()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (&Call{
		scheduler:   scheduler,
		preparation: preparation,
	}).dispatchBody(); err == nil ||
		!strings.Contains(err.Error(), "not prepared for dispatch") {
		t.Fatalf("second Dispatch error = %v", err)
	}
	finalOutcome, err := (&Call{
		scheduler:   scheduler,
		preparation: preparation,
	}).finalize(

		dispatched.Result,
	)
	if err != nil || finalOutcome.Failed() {
		t.Fatalf("final outcome = %#v, error = %v", finalOutcome, err)
	}
	if _, err := (&Call{
		scheduler:   scheduler,
		preparation: preparation,
	}).finalize(

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
	unknown := executeCall(t, testScheduler(t), context.Background(), tools.ToolExecutionInput{
		CallID:    "unknown-1",
		Name:      "absent",
		Arguments: json.RawMessage(`{}`),
	}, state.policies)
	if !unknown.Failed() || !strings.Contains(resultText(t, unknown), "unknown tool") {
		t.Fatalf("unknown outcome = %#v", unknown)
	}
	invalid := objectTool(
		"invalid-output",
		"",
		executorStub(func(
			json.RawMessage,
			tools.ToolRunContext,
		) (json.RawMessage, error) {
			return json.RawMessage(`"wrong"`), nil
		}),
	)
	coordinator := testScheduler(t, invalid)
	invalidOutcome := executeCall(t, coordinator,
		context.Background(),
		tools.ToolExecutionInput{
			CallID:    "invalid-1",
			Name:      "invalid-output",
			Arguments: json.RawMessage(`{}`),
		}, state.policies,
	)
	if !invalidOutcome.Failed() ||
		!strings.Contains(resultText(t, invalidOutcome), "invalid output") {
		t.Fatalf("invalid output outcome = %#v", invalidOutcome)
	}
}

func TestCallerCancellationSurvivesMiddlewareContextReplacement(t *testing.T) {
	dispatchPolicy := &policyFixture[
		tools.ExecuteRequest,
		tools.ExecuteOutcome,
	]{
		name: "replace-context",
		middleware: policyFunc[
			tools.ExecuteRequest,
			tools.ExecuteOutcome,
		](func(
			_ context.Context,
			input tools.ExecuteRequest,
			downstream action[
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
	coordinator := testScheduler(t, objectTool(
		"cancellable",
		"",
		executorStub(func(
			_ json.RawMessage,
			runContext tools.ToolRunContext,
		) (json.RawMessage, error) {
			close(started)
			<-runContext.Context.Done()
			close(settled)
			return json.RawMessage(`{}`), nil
		}),
	))
	callerContext, cancelCaller := context.WithCancel(context.Background())
	resultChannel := make(chan tools.ToolExecutionResult, 1)
	go func() {
		resultChannel <- executeCall(t, coordinator,
			callerContext,
			tools.ToolExecutionInput{
				CallID:    "cancel-1",
				Name:      "cancellable",
				Arguments: json.RawMessage(`{}`),
			}, state.policies,
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
		executorStub(func(
			arguments json.RawMessage,
			runContext tools.ToolRunContext,
		) (json.RawMessage, error) {
			runContext.ConcludeTurn()
			return arguments, nil
		}),
	)
	definition.FinalizeContent = contentFinalizerStub(func(
		tools.ToolExecution,
		tools.ToolResultSnapshot,
	) ([]agentmessage.ContentBlock, bool) {
		finalizeCalls.Add(1)
		return []agentmessage.ContentBlock{
			agentmessage.NewTextBlock("final"),
		}, true
	})
	definition.ConcurrencyBehavior = concurrencyClassifierStub(func(
		json.RawMessage,
	) bool {
		panic("classifier failure")
	})
	coordinator := testScheduler(t, definition)
	if mode := coordinator.ExecutionMode(tools.ToolExecutionInput{
		Name:      "finalized",
		Arguments: json.RawMessage(`{}`),
	}); mode != tools.ExecutionExclusive {
		t.Fatalf("execution mode = %s", mode)
	}
	outcome := executeCall(t, coordinator,
		context.Background(),
		tools.ToolExecutionInput{
			CallID:    "final-1",
			Name:      "finalized",
			Arguments: json.RawMessage(`{}`),
		}, state.policies,
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
