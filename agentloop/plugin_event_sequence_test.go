package agentloop_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agentloop"
	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/tools"
)

type pluginPreStepRejector struct {
	plugin.Base
}

type pluginRequestErrorRetry struct {
	plugin.Base
}

func (extension *pluginRequestErrorRetry) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "agentloop-test-request-error-retry",
		Waterfalls: []plugin.WaterfallMiddlewareBinding{
			plugin.WaterfallOf(extension),
		},
	}
}

func (*pluginRequestErrorRetry) Apply(requestContext context.Context) error {
	return requestContext.Err()
}

func (*pluginRequestErrorRetry) Dispose(context.Context) error { return nil }

func (*pluginRequestErrorRetry) Intercept(
	requestContext context.Context,
	notice agent.RequestErrorNotice,
	downstream plugin.WaterfallAction[
		agent.RequestErrorNotice,
		agent.RequestErrorAction,
	],
) (agent.RequestErrorAction, error) {
	action, err := downstream.Execute(requestContext, notice)
	if err != nil {
		return agent.RequestErrorAction{}, err
	}
	action.Retry = true
	return action, requestContext.Err()
}

func (rejector *pluginPreStepRejector) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "agentloop-test-pre-step-rejector",
		Waterfalls: []plugin.WaterfallMiddlewareBinding{
			plugin.WaterfallOf(rejector),
		},
	}
}

func (*pluginPreStepRejector) Apply(requestContext context.Context) error {
	return requestContext.Err()
}

func (*pluginPreStepRejector) Dispose(context.Context) error {
	return nil
}

func (*pluginPreStepRejector) Intercept(
	requestContext context.Context,
	_ agent.PreStepNotice,
	_ plugin.WaterfallAction[
		agent.PreStepNotice,
		agent.PreStepDecision,
	],
) (agent.PreStepDecision, error) {
	return agent.PreStepDecision{
		Kind: agent.PreStepReject,
	}, requestContext.Err()
}

func TestAgentLoopGoldenEventSequences(t *testing.T) {
	t.Run("fresh", func(t *testing.T) {
		state := newHarnessFixture(t, nil)
		handleState := createTestAgent(t, state, "golden-fresh")
		defer func() {
			if err := handleState.Dispose(context.Background()); err != nil {
				t.Error(err)
			}
		}()
		assertSessionEventNames(
			t,
			handleState.Subject.SessionValue(),
			[]string{},
		)
	})

	completedResponse := []llm.StreamChunk{
		llm.BlockEndChunk{
			Index: 0,
			Block: agentmessage.NewTextBlock("done"),
		},
		llm.FinishChunk{
			Reason: llm.StopFinish{},
		},
	}
	maxTokensResponse := []llm.StreamChunk{
		llm.BlockEndChunk{
			Index: 0,
			Block: agentmessage.NewTextBlock("partial"),
		},
		llm.FinishChunk{
			Reason: llm.MaxTokensFinish{},
		},
	}
	failedResponse := []llm.StreamChunk{
		llm.FinishChunk{
			Reason: llm.ErrorFinish{
				Failure: llm.LlmFailure{
					Message: "upstream failed",
					Code:    "UPSTREAM",
				},
			},
		},
	}
	basePrefix := []string{
		"agent/inbox/spliced",
		"turn/start",
		"agent/inbox/spliced",
	}
	executablePrefix := append(
		append([]string(nil), basePrefix...),
		"step/start",
		"user/message",
		"request/header",
		"request/context",
	)
	type eventScenario struct {
		name           string
		responses      [][]llm.StreamChunk
		extensions     []plugin.Plugin
		configure      func(*testing.T, *harnessFixture)
		wantEvents     []string
		wantTurnResult string
	}
	scenarios := []eventScenario{
		{
			name:       "blocked",
			extensions: []plugin.Plugin{&pluginPreStepRejector{}},
			wantEvents: append(
				append([]string(nil), basePrefix...),
				"turn/end",
			),
			wantTurnResult: "blocked",
		},
		{
			name:      "completed",
			responses: [][]llm.StreamChunk{completedResponse},
			wantEvents: append(
				append([]string(nil), executablePrefix...),
				"assistant/chunk",
				"assistant/chunk",
				"assistant/message",
				"step/end",
				"turn/end",
			),
			wantTurnResult: "completed",
		},
		{
			name:      "max-tokens",
			responses: [][]llm.StreamChunk{maxTokensResponse},
			wantEvents: append(
				append([]string(nil), executablePrefix...),
				"assistant/chunk",
				"assistant/chunk",
				"assistant/message",
				"step/end",
				"turn/end",
			),
			wantTurnResult: "max-tokens",
		},
		{
			name: "retry",
			responses: [][]llm.StreamChunk{
				failedResponse,
				completedResponse,
			},
			extensions: []plugin.Plugin{
				&pluginRequestErrorRetry{},
			},
			wantEvents: append(
				append([]string(nil), executablePrefix...),
				"assistant/chunk",
				"assistant/chunk",
				"assistant/chunk",
				"assistant/message",
				"step/end",
				"turn/end",
			),
			wantTurnResult: "completed",
		},
		{
			name:      "llm-failure",
			responses: [][]llm.StreamChunk{failedResponse},
			wantEvents: append(
				append([]string(nil), executablePrefix...),
				"assistant/chunk",
				"step/end",
				"turn/end",
			),
			wantTurnResult: "error",
		},
		{
			name: "tool-failure",
			responses: [][]llm.StreamChunk{
				toolCallResponse("failed-call"),
				completedResponse,
			},
			configure: func(t *testing.T, state *harnessFixture) {
				registerParallelTool(t, state, func(
					json.RawMessage,
					tools.ToolRunContext,
				) (json.RawMessage, error) {
					return nil, errors.New("tool body failed")
				})
			},
			wantEvents: append(
				append([]string(nil), executablePrefix...),
				"assistant/chunk",
				"assistant/chunk",
				"assistant/message",
				"tool/call",
				"tool/result",
				"step/end",
				"step/start",
				"assistant/chunk",
				"assistant/chunk",
				"assistant/message",
				"step/end",
				"turn/end",
			),
			wantTurnResult: "completed",
		},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			state := newHarnessFixtureWithSettings(
				t,
				scenario.responses,
				agentloopSettings(),
				scenario.extensions...,
			)
			if scenario.configure != nil {
				scenario.configure(t, state)
			}
			handleState := createTestAgent(
				t,
				state,
				session.SessionID("golden-"+scenario.name),
			)
			defer func() {
				if err := handleState.Dispose(context.Background()); err != nil {
					t.Error(err)
				}
			}()
			if err := handleState.Subject.Followup(
				userMessage(t, scenario.name),
			); err != nil {
				t.Fatal(err)
			}
			waitForIdle(t, handleState.Subject)
			assertSessionEventNames(
				t,
				handleState.Subject.SessionValue(),
				scenario.wantEvents,
			)
			ending := lastTurnEnd(t, handleState.Subject.SessionValue())
			if ending.Kind != scenario.wantTurnResult {
				t.Fatalf(
					"turn result = %q, want %q",
					ending.Kind,
					scenario.wantTurnResult,
				)
			}
			assertAgentLoopBoundariesPaired(
				t,
				handleState.Subject.SessionValue(),
			)
		})
	}
}

func TestEachModelDispatchRebuildsFromFreshSessionPrefix(t *testing.T) {
	state := newHarnessFixture(t, modelResponses())
	registerEchoTool(t, state)
	handleState := createTestAgent(t, state, "request-reconstruction")
	defer func() {
		if err := handleState.Dispose(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	state.backend.capturePrefixes(handleState.Subject.SessionValue().Events)
	if err := handleState.Subject.Followup(
		userMessage(t, "reconstruct"),
	); err != nil {
		t.Fatal(err)
	}
	waitForIdle(t, handleState.Subject)
	requests := state.backend.snapshots()
	prefixes := state.backend.prefixSnapshots()
	if len(requests) != 2 || len(prefixes) != len(requests) {
		t.Fatalf(
			"captured requests/prefixes = %d/%d, want 2/2",
			len(requests),
			len(prefixes),
		)
	}
	for index, request := range requests {
		fresh, err := session.New(
			session.SessionID(request.SessionID),
			session.CreateOptions{
				Seed: prefixes[index],
			},
		)
		if err != nil {
			t.Fatalf("request %d fresh Session: %v", index, err)
		}
		header, err := session.LatestRequestHeader(fresh.Events())
		if err != nil {
			t.Fatalf("request %d header: %v", index, err)
		}
		if header == nil {
			t.Fatalf("request %d has no reconstructed header", index)
		}
		messages, err := fresh.DeriveMessages()
		if err != nil {
			t.Fatalf("request %d messages: %v", index, err)
		}
		reconstructed := llm.GenerateOptions{
			CallConfig: header.Config,
			Messages:   messages,
			System:     header.System,
			Tools:      header.Tools,
			SessionID:  request.SessionID,
		}
		if !reflect.DeepEqual(reconstructed, request) {
			t.Fatalf(
				"request %d reconstruction = %#v, want %#v",
				index,
				reconstructed,
				request,
			)
		}
	}
}

func agentloopSettings() agentloop.Settings {
	return agentloop.Settings{
		MaxParallelToolCalls: agentloop.DefaultMaxParallelToolCalls,
	}
}

func assertSessionEventNames(
	t *testing.T,
	conversation session.Context,
	want []string,
) {
	t.Helper()
	events := conversation.Events()
	got := make([]string, len(events))
	for index, committed := range events {
		got[index] = committed.Type
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Session event sequence = %#v, want %#v", got, want)
	}
}
