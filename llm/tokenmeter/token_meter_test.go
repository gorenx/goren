package tokenmeter

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
)

func TestTokenMeterMeasuresEmptyAndDetachedSurface(t *testing.T) {
	t.Parallel()
	conversation := newConversation(t, "empty")
	meterService := new(TokenMeter)
	empty, err := meterService.Measure(context.Background(), conversation.Events(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if empty.LogRevision != 0 || empty.Baseline.Kind != BaselineNone ||
		empty.TotalTokens != 0 || empty.SurfaceTokens != 0 || len(empty.Nodes) != 0 {
		t.Fatalf("empty measurement = %#v", empty)
	}
	appendUser(t, conversation, "first")
	first, err := meterService.Measure(context.Background(), conversation.Events(), nil)
	if err != nil {
		t.Fatal(err)
	}
	firstSnapshot := append([]SurfaceNode(nil), first.Nodes...)
	appendUser(t, conversation, "second")
	second, err := meterService.Measure(context.Background(), conversation.Events(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Nodes) != 1 || !reflect.DeepEqual(first.Nodes, firstSnapshot) {
		t.Fatalf("first measurement mutated = %#v", first)
	}
	if second.LogRevision != 2 || len(second.Nodes) != 2 ||
		sumNodeTokens(second.Nodes) != second.SurfaceTokens {
		t.Fatalf("advanced measurement = %#v", second)
	}
}

func TestTokenMeterPricesHeaderAndRequestOverrideWithoutChangingSurface(t *testing.T) {
	t.Parallel()
	conversation := newConversation(t, "header")
	appendUser(t, conversation, "question")
	prompt := "system"
	headerValue := fixtureHeader("model-a")
	headerValue.System = &prompt
	headerValue.Tools = []llm.ToolSchema{
		{
			Name:        "read",
			Description: "read",
			Parameters:  []byte(`{"type":"object"}`),
		},
	}
	{
		draft, err := session.NewEventDraft(session.RequestHeaderSet,
			session.RequestHeaderSnapshot{
				Header: headerValue,
				Reason: session.RequestHeaderInitial,
			})
		if err == nil {
			_, err = conversation.Commit(context.Background(), session.Batch(draft))
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	meterService := new(TokenMeter)
	logged, err := meterService.Measure(context.Background(), conversation.Events(), nil)
	if err != nil {
		t.Fatal(err)
	}
	largePrompt := strings.Repeat("large override ", 100)
	override := fixtureHeader("model-b")
	override.System = &largePrompt
	overridden, err := meterService.Measure(context.Background(), conversation.Events(), &override)
	if err != nil {
		t.Fatal(err)
	}
	if logged.Baseline.Kind != BaselineEstimated ||
		overridden.TotalTokens <= logged.TotalTokens ||
		overridden.SurfaceTokens != logged.SurfaceTokens ||
		!reflect.DeepEqual(overridden.Nodes, logged.Nodes) {
		t.Fatalf("logged = %#v, overridden = %#v", logged, overridden)
	}
}

func TestTokenMeterUsesUsageAnchorAndDurableRewriteDelta(t *testing.T) {
	t.Parallel()
	conversation := newConversation(t, "usage-anchor")
	appendUser(t, conversation, "before")
	usageValue := llm.TokenUsage{
		InputTokens:  20,
		OutputTokens: 7,
	}
	cacheRead := int64(3)
	cacheWrite := int64(4)
	usageValue.CacheReadTokens = &cacheRead
	usageValue.CacheWriteTokens = &cacheWrite
	appendSuccessfulCall(
		t,
		conversation,
		fixtureHeader("model-a"),
		"short",
		"a much longer rewritten durable assistant answer",
		&usageValue,
	)
	meterService := new(TokenMeter)
	measured, err := meterService.Measure(context.Background(), conversation.Events(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if measured.Baseline.Kind != BaselineUsage || measured.Baseline.Tokens != 34 ||
		measured.SurfaceDeltaTokens <= 0 ||
		measured.TotalTokens != 34+measured.SurfaceDeltaTokens {
		t.Fatalf("measurement = %#v", measured)
	}
	if measured.Baseline.Usage == nil {
		t.Fatal("usage baseline omitted provider usage")
	}
	measured.Baseline.Usage.InputTokens = 1
	again, err := meterService.Measure(context.Background(), conversation.Events(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if again.Baseline.Usage == nil || again.Baseline.Usage.InputTokens != 20 {
		t.Fatalf("measurement aliases Session usage = %#v", again.Baseline.Usage)
	}
}

func TestTokenMeterTreatsExplicitEmptyAndAbsentProvenanceDifferently(t *testing.T) {
	t.Parallel()
	usageValue := llm.TokenUsage{
		InputTokens:  20,
		OutputTokens: 20,
	}
	explicit := newConversation(t, "explicit")
	appendCallWithProvenance(
		t,
		explicit,
		fixtureHeader("model-a"),
		"listener injected text",
		&usageValue,
		pointerToSeqs([]int64{}),
	)
	legacy := newConversation(t, "legacy")
	appendCallWithProvenance(
		t,
		legacy,
		fixtureHeader("model-a"),
		"listener injected text",
		&usageValue,
		nil,
	)
	meterService := new(TokenMeter)
	explicitMeasurement, err := meterService.Measure(context.Background(), explicit.Events(), nil)
	if err != nil {
		t.Fatal(err)
	}
	legacyMeasurement, err := new(TokenMeter).Measure(context.Background(), legacy.Events(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if explicitMeasurement.SurfaceDeltaTokens <= 0 || legacyMeasurement.SurfaceDeltaTokens != 0 {
		t.Fatalf(
			"explicit delta = %d, legacy delta = %d",
			explicitMeasurement.SurfaceDeltaTokens,
			legacyMeasurement.SurfaceDeltaTokens,
		)
	}
}

func TestTokenMeterRejectsMalformedStepTransactionally(t *testing.T) {
	t.Parallel()
	conversation := newConversation(t, "bad-step")
	assistantValue := newAssistantMessage(t, "bad")
	emptySources := []int64{}
	{
		draft, err := session.NewSurfaceEventDraft(session.AssistantMessaged,
			session.AssistantMessage{
				Turn:    1,
				Step:    1,
				Message: assistantValue,
			},
			session.SurfaceIntent{
				Operation:       session.SurfaceAppend(),
				SourceEventSeqs: &emptySources,
			})
		if err == nil {
			_, err = conversation.Commit(context.Background(), session.Batch(draft))
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	meterService := new(TokenMeter)
	for attempt := 0; attempt < 2; attempt++ {
		_, err := meterService.Measure(context.Background(), conversation.Events(), nil)
		if err == nil || !strings.Contains(err.Error(), "no matching step/start") {
			t.Fatalf("attempt %d error = %v", attempt, err)
		}
	}
}

func TestTokenMeterFallsBackWhenProviderUsageUndercutsHeuristicAnchor(t *testing.T) {
	t.Parallel()
	conversation := newConversation(t, "low-usage")
	prompt := strings.Repeat("system context ", 128)
	headerValue := fixtureHeader("model-a")
	headerValue.System = &prompt
	usageValue := llm.TokenUsage{
		InputTokens:  1,
		OutputTokens: 1,
	}
	appendSuccessfulCall(
		t,
		conversation,
		headerValue,
		strings.Repeat("provider answer ", 256),
		strings.Repeat("provider answer ", 256),
		&usageValue,
	)
	measured, err := new(TokenMeter).Measure(context.Background(), conversation.Events(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if measured.Baseline.Kind != BaselineEstimated || measured.Baseline.Tokens <= 2 {
		t.Fatalf("low-usage baseline = %#v", measured.Baseline)
	}
}

func TestTokenMeterReusesUsageOnlyForMatchingCanonicalHeader(t *testing.T) {
	t.Parallel()
	conversation := newConversation(t, "header-anchor")
	headerValue := fixtureHeader("model-a")
	usageValue := llm.TokenUsage{
		InputTokens:  100,
		OutputTokens: 50,
	}
	appendSuccessfulCall(
		t,
		conversation,
		headerValue,
		"answer",
		"answer",
		&usageValue,
	)
	meterService := new(TokenMeter)
	matching, err := meterService.Measure(context.Background(), conversation.Events(), &headerValue)
	if err != nil {
		t.Fatal(err)
	}
	if matching.Baseline.Kind != BaselineUsage {
		t.Fatalf("matching baseline = %#v", matching.Baseline)
	}
	changedPrompt := "different"
	changed := fixtureHeader("model-a")
	changed.System = &changedPrompt
	nonmatching, err := meterService.Measure(context.Background(), conversation.Events(), &changed)
	if err != nil {
		t.Fatal(err)
	}
	if nonmatching.Baseline.Kind != BaselineEstimated || nonmatching.SurfaceDeltaTokens != 0 {
		t.Fatalf("nonmatching baseline = %#v", nonmatching)
	}
}

func TestTokenMeterPricesEmptyAssistantSurfaceNodeAtZero(t *testing.T) {
	t.Parallel()
	conversation := newConversation(t, "empty-assistant")
	appendSuccessfulCall(
		t,
		conversation,
		fixtureHeader("model-a"),
		"",
		"",
		nil,
	)
	measured, err := new(TokenMeter).Measure(context.Background(), conversation.Events(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(measured.Nodes) != 1 || measured.Nodes[0].Tokens != 0 ||
		measured.SurfaceTokens != 0 {
		t.Fatalf("empty assistant measurement = %#v", measured)
	}
}

func TestTokenMeterRejectsOverlappingAndMismatchedStepBoundaries(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name      string
		populate  func(*testing.T, *session.Log)
		wantError string
	}{
		{
			name: "overlapping-start",
			populate: func(testingContext *testing.T, conversation *session.Log) {
				for _, position := range []session.StepPosition{
					{
						Turn: 1,
						Step: 1,
					},
					{
						Turn: 1,
						Step: 2,
					},
				} {
					{
						draft, err := session.NewEventDraft(session.StepStarted,
							position)
						if err == nil {
							_, err = conversation.Commit(context.Background(), session.Batch(draft))
						}
						if err != nil {
							testingContext.Fatal(err)
						}
					}
				}
			},
			wantError: "arrived before turn 1/step 1 ended",
		},
		{
			name: "mismatched-end",
			populate: func(testingContext *testing.T, conversation *session.Log) {
				{
					draft, err := session.NewEventDraft(session.StepStarted,
						session.StepPosition{
							Turn: 1,
							Step: 1,
						})
					if err == nil {
						_, err = conversation.Commit(context.Background(), session.Batch(draft))
					}
					if err != nil {
						testingContext.Fatal(err)
					}
				}
				{
					draft, err := session.NewEventDraft(session.StepEnded,
						session.StepPosition{
							Turn: 1,
							Step: 2,
						})
					if err == nil {
						_, err = conversation.Commit(context.Background(), session.Batch(draft))
					}
					if err != nil {
						testingContext.Fatal(err)
					}
				}
			},
			wantError: "has no matching step/start event",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			conversation := newConversation(t, session.SessionID(testCase.name))
			testCase.populate(t, conversation)
			meterService := new(TokenMeter)
			for attempt := 0; attempt < 2; attempt++ {
				_, err := meterService.Measure(context.Background(), conversation.Events(), nil)
				if err == nil || !strings.Contains(err.Error(), testCase.wantError) {
					t.Fatalf("attempt %d error = %v", attempt, err)
				}
			}
		})
	}
}

func newConversation(testingContext *testing.T, identifier session.SessionID) *session.Log {
	testingContext.Helper()
	conversation, err := session.NewLog(identifier, session.CreateOptions{})
	if err != nil {
		testingContext.Fatal(err)
	}
	return conversation
}

func fixtureHeader(modelID string) session.EpochHeader {
	return session.CanonicalEpochHeader(session.EpochHeader{
		Config: llm.CallConfig{
			Provider: "mock",
			Model:    modelID,
		},
	})
}

func appendUser(
	testingContext *testing.T,
	conversation *session.Log,
	textValue string,
) int64 {
	testingContext.Helper()
	messageValue := mustUserMessage(
		testingContext,
		[]agentmessage.ContentBlock{
			agentmessage.NewTextBlock(textValue),
		},
	)
	draft, err := session.NewSurfaceEventDraft(session.UserMessageAdded,
		messageValue,
		session.SurfaceIntent{
			Operation: session.SurfaceAppend(),
		})
	if err != nil {
		testingContext.Fatal(err)
	}
	receipt, err := conversation.Commit(context.Background(), session.Batch(draft))
	if err != nil {
		testingContext.Fatal(err)
	}
	return receipt.Events[0].Seq
}

func appendSuccessfulCall(
	testingContext *testing.T,
	conversation *session.Log,
	headerValue session.EpochHeader,
	providerText string,
	durableText string,
	usageValue *llm.TokenUsage,
) {
	testingContext.Helper()
	{
		draft, err := session.NewEventDraft(session.StepStarted,
			session.StepPosition{
				Turn: 1,
				Step: 1,
			})
		if err == nil {
			_, err = conversation.Commit(context.Background(), session.Batch(draft))
		}
		if err != nil {
			testingContext.Fatal(err)
		}
	}
	{
		draft, err := session.NewEventDraft(session.RequestHeaderSet,
			session.RequestHeaderSnapshot{
				Header: headerValue,
				Reason: session.RequestHeaderInitial,
			})
		if err == nil {
			_, err = conversation.Commit(context.Background(), session.Batch(draft))
		}
		if err != nil {
			testingContext.Fatal(err)
		}
	}
	sources := make([]int64, 0, 3)
	chunks := []llm.StreamChunk{
		llm.BlockStartChunk{
			Index:     0,
			BlockType: "text",
		},
		llm.TextDeltaChunk{
			Index: 0,
			Text:  providerText,
		},
		llm.BlockEndChunk{
			Index: 0,
			Block: agentmessage.NewTextBlock(providerText),
		},
	}
	for _, chunkValue := range chunks {
		committed, err := session.Event{}, error(nil)
		{
			var committedEvent session.Event
			var writeErr error
			draft, draftErr := session.NewEventDraft(session.AssistantChunked,
				session.AssistantChunk{
					Turn:  1,
					Step:  1,
					Chunk: chunkValue,
				})
			writeErr = draftErr
			if draftErr == nil {
				receipt, commitErr := conversation.Commit(context.Background(), session.Batch(draft))
				writeErr = commitErr
				if commitErr == nil {
					committedEvent = receipt.Events[0]
				}
			}
			committed = committedEvent
			err = writeErr
		}

		if err != nil {
			testingContext.Fatal(err)
		}
		sources = append(sources, committed.Seq)
	}
	appendAssistantWithSources(
		testingContext,
		conversation,
		durableText,
		usageValue,
		&sources,
	)
	{
		draft, err := session.NewEventDraft(session.StepEnded,
			session.StepPosition{
				Turn: 1,
				Step: 1,
			})
		if err == nil {
			_, err = conversation.Commit(context.Background(), session.Batch(draft))
		}
		if err != nil {
			testingContext.Fatal(err)
		}
	}
}

func appendCallWithProvenance(
	testingContext *testing.T,
	conversation *session.Log,
	headerValue session.EpochHeader,
	durableText string,
	usageValue *llm.TokenUsage,
	sourceSeqs *[]int64,
) {
	testingContext.Helper()
	{
		draft, err := session.NewEventDraft(session.StepStarted,
			session.StepPosition{
				Turn: 1,
				Step: 1,
			})
		if err == nil {
			_, err = conversation.Commit(context.Background(), session.Batch(draft))
		}
		if err != nil {
			testingContext.Fatal(err)
		}
	}
	{
		draft, err := session.NewEventDraft(session.RequestHeaderSet,
			session.RequestHeaderSnapshot{
				Header: headerValue,
				Reason: session.RequestHeaderInitial,
			})
		if err == nil {
			_, err = conversation.Commit(context.Background(), session.Batch(draft))
		}
		if err != nil {
			testingContext.Fatal(err)
		}
	}
	appendAssistantWithSources(
		testingContext,
		conversation,
		durableText,
		usageValue,
		sourceSeqs,
	)
	{
		draft, err := session.NewEventDraft(session.StepEnded,
			session.StepPosition{
				Turn: 1,
				Step: 1,
			})
		if err == nil {
			_, err = conversation.Commit(context.Background(), session.Batch(draft))
		}
		if err != nil {
			testingContext.Fatal(err)
		}
	}
}

func appendAssistantWithSources(
	testingContext *testing.T,
	conversation *session.Log,
	textValue string,
	usageValue *llm.TokenUsage,
	sourceSeqs *[]int64,
) int64 {
	testingContext.Helper()
	assistantValue := newAssistantMessage(testingContext, textValue)
	committed, err := session.Event{}, error(nil)
	{
		var committedEvent session.Event
		var writeErr error
		draft, draftErr := session.NewSurfaceEventDraft(session.AssistantMessaged,
			session.AssistantMessage{
				Turn:    1,
				Step:    1,
				Message: assistantValue,
				Usage:   usageValue,
			},
			session.SurfaceIntent{
				Operation:       session.SurfaceAppend(),
				SourceEventSeqs: sourceSeqs,
			})
		writeErr = draftErr
		if draftErr == nil {
			receipt, commitErr := conversation.Commit(context.Background(), session.Batch(draft))
			writeErr = commitErr
			if commitErr == nil {
				committedEvent = receipt.Events[0]
			}
		}
		committed = committedEvent
		err = writeErr
	}

	if err != nil {
		testingContext.Fatal(err)
	}
	return committed.Seq
}

func newAssistantMessage(testingContext *testing.T, textValue string) agentmessage.AssistantMessage {
	testingContext.Helper()
	content := []agentmessage.ContentBlock{}
	if textValue != "" {
		content = append(content, agentmessage.NewTextBlock(textValue))
	}
	messageValue, err := agentmessage.NewAssistantMessage(agentmessage.AssistantMessageInput{
		Content: content,
		Source: agentmessage.ModelMessageSource{
			Provider: "mock",
			Model:    "model-a",
		},
	})
	if err != nil {
		testingContext.Fatal(err)
	}
	return messageValue
}

func pointerToSeqs(values []int64) *[]int64 {
	detached := append([]int64(nil), values...)
	return &detached
}

func sumNodeTokens(nodes []SurfaceNode) int64 {
	var total int64
	for _, nodeValue := range nodes {
		total += nodeValue.Tokens
	}
	return total
}

func TestMeasurementFailureDoesNotInstallPartialReplay(t *testing.T) {
	conversation := newConversation(t, "atomic")
	appendUser(t, conversation, "first")
	pricing := new(TokenMeter)
	first, err := pricing.Measure(context.Background(), conversation.Events(), nil)
	if err != nil {
		t.Fatal(err)
	}
	appendUser(t, conversation, "second")
	malformed := append(conversation.Events(), session.Event{
		Seq:  2,
		Type: session.StepEndEventName,
	})
	if _, err := pricing.Measure(context.Background(), malformed, nil); err == nil {
		t.Fatal("invalid step accepted")
	}
	if pricing.fold.consumedEvents != first.LogRevision || !reflect.DeepEqual(pricing.fold.nodes, first.Nodes) {
		t.Fatal("failed measurement installed a partial replay")
	}
	second, err := pricing.Measure(context.Background(), conversation.Events(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.LogRevision != 2 || len(second.Nodes) != 2 {
		t.Fatalf("retry = %#v", second)
	}
	historical, err := pricing.Measure(context.Background(), conversation.Events()[:1], nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(historical, first) || pricing.fold.consumedEvents != 2 {
		t.Fatalf("historical measurement regressed online state: %#v", historical)
	}
}
