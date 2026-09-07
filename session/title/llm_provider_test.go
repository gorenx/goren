package title

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

var titleLLMFixtureConfig = LLMConfig{
	Provider:    "title-provider",
	Model:       "title-model",
	TargetWords: 5, TargetCJKCharacters: 10, MaxInputBytes: 4096,
	MaxOutputTokens: 64, TimeoutMS: 1000,
}

type titleLLMFixtureRuntime struct {
	stream func(context.Context, llm.GenerateOptions) (llm.ChunkStream, error)
}

func (runtime *titleLLMFixtureRuntime) Stream(
	requestContext context.Context,
	requestOptions llm.GenerateOptions,
) (llm.ChunkStream, error) {
	return runtime.stream(requestContext, requestOptions)
}

func TestLLMProviderSelectsMessagesRecordsRequestAndReturnsText(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		label          string
		newProvider    func(LLMStreamer, LLMConfig) (*LLMProvider, error)
		wantProvider   ProviderID
		wantMessageSeq []int64
	}{
		{
			label: "first prompt", newProvider: NewFirstPromptLLMProvider,
			wantProvider: FirstPromptLLMProviderID, wantMessageSeq: []int64{3},
		},
		{
			label: "all prompts", newProvider: NewAllPromptsLLMProvider,
			wantProvider: AllPromptsLLMProviderID, wantMessageSeq: []int64{3, 8},
		},
	} {
		testCase := testCase
		t.Run(testCase.label, func(t *testing.T) {
			t.Parallel()
			var seen llm.GenerateOptions
			runtime := &titleLLMFixtureRuntime{stream: func(
				_ context.Context,
				requestOptions llm.GenerateOptions,
			) (llm.ChunkStream, error) {
				seen = requestOptions
				return llm.NewSliceStream([]llm.StreamChunk{
					llm.BlockEndChunk{Type: "block-end", Index: 0, Block: agentmessage.NewTextBlock("  Generated\t title  ")},
					llm.FinishChunk{Type: "finish", Reason: llm.StopFinish{Kind: "stop"}},
				})
			}}
			implementation, err := testCase.newProvider(runtime, titleLLMFixtureConfig)
			if err != nil {
				t.Fatal(err)
			}
			conversation, err := session.NewLog("title-llm", session.CreateOptions{})
			if err != nil {
				t.Fatal(err)
			}
			result, err := generateTitle(t, conversation, implementation, context.Background(), ProviderRequest{
				SessionID: conversation.Header().ID,
				Messages:  []UserMessage{{Seq: 3, Text: "First prompt"}, {Seq: 8, Text: "Second prompt"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.Title != "Generated title" || !equalInt64s(result.MessageSeqs, testCase.wantMessageSeq) ||
				result.Model == nil || result.Model.Provider != "title-provider" || result.Model.Model != "title-model" {
				t.Fatalf("provider result = %#v", result)
			}
			if implementation.ID() != testCase.wantProvider || seen.Purpose != llm.PurposeSessionTitle ||
				seen.SessionID != "title-llm" || seen.Provider != "title-provider" || seen.Model != "title-model" ||
				seen.MaxTokens == nil || *seen.MaxTokens != 64 || len(seen.Messages) != 1 || seen.System == nil {
				t.Fatalf("generate options = %#v", seen)
			}
			textValue := seen.Messages[0].ContentValue()[0].(agentmessage.TextBlock).Text
			if !strings.Contains(textValue, `{"seq":3,"text":"First prompt"}`) ||
				(testCase.wantProvider == FirstPromptLLMProviderID && strings.Contains(textValue, "Second prompt")) {
				t.Fatalf("framed input = %q", textValue)
			}
			events := conversation.Events()
			if len(events) != 1 || events[0].Type != TitleLLMRequestEventName {
				t.Fatalf("request events = %#v", events)
			}
			var recorded struct {
				TitleProvider ProviderID      `json:"titleProvider"`
				MessageSeqs   []int64         `json:"messageSeqs"`
				Route         ModelProvenance `json:"route"`
				MaxTokens     int             `json:"maxTokens"`
			}
			if err := json.Unmarshal(events[0].Data, &recorded); err != nil {
				t.Fatal(err)
			}
			if recorded.TitleProvider != testCase.wantProvider ||
				!equalInt64s(recorded.MessageSeqs, testCase.wantMessageSeq) ||
				recorded.Route.Provider != "title-provider" || recorded.MaxTokens != 64 {
				t.Fatalf("recorded request = %#v", recorded)
			}
		})
	}
}

func TestLLMProviderUsesExplicitRouteAndRejectsBeforeDispatch(t *testing.T) {
	t.Parallel()
	calls := 0
	runtime := &titleLLMFixtureRuntime{stream: func(
		context.Context,
		llm.GenerateOptions,
	) (llm.ChunkStream, error) {
		calls++
		return nil, errors.New("unexpected dispatch")
	}}
	settings := titleLLMFixtureConfig
	settings.Provider = "title-route"
	settings.Model = "title-model"
	settings.MaxInputBytes = 1
	implementation, err := NewFirstPromptLLMProvider(runtime, settings)
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := session.NewLog("title-budget", session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = generateTitle(t, conversation, implementation, context.Background(), ProviderRequest{
		SessionID: conversation.Header().ID, Messages: []UserMessage{{Seq: 0, Text: "prompt"}},
	})
	if err == nil || !strings.Contains(err.Error(), "exceeding maxInputBytes") {
		t.Fatalf("budget error = %v", err)
	}
	if calls != 0 || len(conversation.Events()) != 0 {
		t.Fatalf("pre-dispatch side effects = calls %d events %#v", calls, conversation.Events())
	}
}

func TestLLMProviderRejectsNonStopAndHonorsTimeout(t *testing.T) {
	t.Parallel()
	conversation, err := session.NewLog("title-errors", session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	maxTokensRuntime := &titleLLMFixtureRuntime{stream: func(
		context.Context,
		llm.GenerateOptions,
	) (llm.ChunkStream, error) {
		return llm.NewSliceStream([]llm.StreamChunk{
			llm.FinishChunk{Type: "finish", Reason: llm.MaxTokensFinish{Kind: "max-tokens"}},
		})
	}}
	implementation, err := NewFirstPromptLLMProvider(maxTokensRuntime, titleLLMFixtureConfig)
	if err != nil {
		t.Fatal(err)
	}
	_, err = generateTitle(t, conversation, implementation, context.Background(), ProviderRequest{
		SessionID: conversation.Header().ID, Messages: []UserMessage{{Seq: 0, Text: "prompt"}},
	})
	if err == nil || !strings.Contains(err.Error(), "maxOutputTokens") {
		t.Fatalf("finish error = %v", err)
	}

	timeoutRuntime := &titleLLMFixtureRuntime{stream: func(
		requestContext context.Context,
		_ llm.GenerateOptions,
	) (llm.ChunkStream, error) {
		return &titleBlockingStream{requestContext: requestContext}, nil
	}}
	timeoutSettings := titleLLMFixtureConfig
	timeoutSettings.TimeoutMS = 20
	timeoutProvider, err := NewFirstPromptLLMProvider(timeoutRuntime, timeoutSettings)
	if err != nil {
		t.Fatal(err)
	}
	_, err = generateTitle(t, conversation, timeoutProvider, context.Background(), ProviderRequest{
		SessionID: conversation.Header().ID, Messages: []UserMessage{{Seq: 0, Text: "prompt"}},
	})
	var timeoutProblem *titleLLMTimeoutError
	if !errors.As(err, &timeoutProblem) || timeoutProblem.Code() != TitleLLMTimeoutCode {
		t.Fatalf("timeout error = %T %v", err, err)
	}
}

func TestLLMConfigRequiresCompletePositivePolicy(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		label    string
		settings LLMConfig
	}{
		{label: "missing", settings: LLMConfig{}},
		{label: "provider only", settings: LLMConfig{
			TargetWords: 1, TargetCJKCharacters: 1, MaxInputBytes: 1,
			MaxOutputTokens: 1, TimeoutMS: 1, Provider: "p",
		}},
	} {
		if _, err := testCase.settings.Validate(); err == nil {
			t.Fatalf("%s config was accepted", testCase.label)
		}
	}
}

type titleBlockingStream struct {
	requestContext context.Context
}

func (flow *titleBlockingStream) Next(context.Context) (llm.StreamChunk, bool, error) {
	<-flow.requestContext.Done()
	return nil, false, flow.requestContext.Err()
}

func (*titleBlockingStream) Close(context.Context) error { return nil }

func equalInt64s(left []int64, right []int64) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func generateTitle(t *testing.T, conversation *session.Log, implementation Provider, requestContext context.Context, request ProviderRequest) (ProviderResult, error) {
	t.Helper()
	prepared, err := implementation.Prepare(request)
	if err != nil {
		return ProviderResult{}, err
	}
	if _, err := conversation.Commit(requestContext, session.Batch(prepared.Draft())); err != nil {
		return ProviderResult{}, err
	}
	return prepared.Generate(requestContext)
}

func TestPrepareDoesNotDispatchOrRetainInputAliases(t *testing.T) {
	calls := 0
	runtime := &titleLLMFixtureRuntime{
		stream: func(_ context.Context, options llm.GenerateOptions) (llm.ChunkStream, error) {
			calls++
			if options.Provider != "title-provider" {
				t.Fatal("prepared route changed")
			}
			return llm.NewSliceStream([]llm.StreamChunk{
				llm.BlockEndChunk{
					Type:  "block-end",
					Index: 0,
					Block: agentmessage.NewTextBlock("generated"),
				},
				llm.FinishChunk{
					Type:   "finish",
					Reason: llm.StopFinish{Kind: "stop"},
				},
			})
		},
	}
	implementation, err := NewFirstPromptLLMProvider(runtime, titleLLMFixtureConfig)
	if err != nil {
		t.Fatal(err)
	}
	request := ProviderRequest{
		SessionID: "title",
		Messages: []UserMessage{{
			Seq:  1,
			Text: "original input",
		}},
	}
	prepared, err := implementation.Prepare(request)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("preparation dispatched LLM")
	}
	request.Messages[0].Seq = 99
	conversation, err := session.NewLog("title", session.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conversation.Commit(context.Background(), session.Batch(prepared.Draft())); err != nil {
		t.Fatal(err)
	}
	result, err := prepared.Generate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !equalInt64s(result.MessageSeqs, []int64{1}) {
		t.Fatalf("result = %#v, calls = %d", result, calls)
	}
}
